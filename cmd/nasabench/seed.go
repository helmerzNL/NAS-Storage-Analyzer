package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

// extPick is one row of the extension distribution: files whose random draw
// falls in [lo, hi) get this extension and a size of 10^(minExp..maxExp).
const extPick = `(VALUES
	(0.000, 0.080, 'mkv',  7.5, 10.0), (0.080, 0.140, 'mp4',  7.0, 9.5), (0.140, 0.150, 'avi',  7.0, 9.0),
	(0.150, 0.270, 'mp3',  6.0, 7.5), (0.270, 0.320, 'flac', 7.0, 8.3), (0.320, 0.520, 'jpg',  5.0, 7.0),
	(0.520, 0.570, 'cr2',  7.0, 7.7), (0.570, 0.620, 'png',  4.5, 6.5), (0.620, 0.700, 'pdf',  4.5, 7.5),
	(0.700, 0.730, 'docx', 4.0, 6.5), (0.730, 0.780, 'txt',  2.0, 5.0), (0.780, 0.810, 'zip',  5.0, 9.0),
	(0.810, 0.813, 'iso',  9.0, 10.0), (0.813, 0.840, 'py',   2.0, 4.5), (0.840, 0.890, '',     2.0, 6.0),
	(0.890, 1.001, 'dat',  2.0, 7.0)
) AS p(lo, hi, ext, min_exp, max_exp)`

func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	dsn := fs.String("db", os.Getenv("NASA_DB_DSN"), "PostgreSQL connection string")
	name := fs.String("share", "bench", "share name to (re)create")
	files := fs.Int64("files", 10_000_000, "number of files")
	dirs := fs.Int64("dirs", 200_000, "number of directories")
	fanout := fs.Int64("fanout", 6, "children per directory (tree depth follows from dirs and fanout)")
	chunk := fs.Int64("chunk", 1_000_000, "files per insert statement")
	fs.Parse(args)
	if *dsn == "" {
		return errors.New("pass -db or set NASA_DB_DSN")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st, err := store.Open(ctx, *dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	sh, err := st.EnsureShare(ctx, *name, "/synthetic/"+*name, nil, 1, 0)
	if err != nil {
		return err
	}
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	exec := func(label, sql string, a ...any) error {
		t := time.Now()
		tag, err := conn.Exec(ctx, sql, a...)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		fmt.Printf("%-34s %8.1fs  %d rows\n", label, time.Since(t).Seconds(), tag.RowsAffected())
		return nil
	}
	id := sh.ID
	part := func(t string) string { return fmt.Sprintf("%s_s%d", t, id) }

	if err := exec("truncate share", fmt.Sprintf("TRUNCATE %s, %s, %s, %s", part("files"), part("dirs"), part("dir_category_agg"), part("share_ext_agg"))); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT setseed(0.42)`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SET maintenance_work_mem = '1GB'; SET max_parallel_maintenance_workers = 3`); err != nil {
		return err
	}

	// Directories: a complete k-ary tree, parent(id) = (id-2)/k + 1.
	if err := exec("insert directories", `
		WITH RECURSIVE t AS (
			SELECT 1::bigint AS id, NULL::bigint AS parent_id, 0 AS depth, '1'::text AS path
			UNION ALL
			SELECT n.id, (n.id - 2) / $3 + 1, t.depth + 1, t.path || '.' || n.id
			FROM generate_series(2::bigint, $2::bigint) AS n(id) JOIN t ON t.id = (n.id - 2) / $3 + 1)
		INSERT INTO dirs (share_id, id, parent_id, name, depth, path, mtime, entry_count, last_seen_scan)
		SELECT $1, id, parent_id, CASE WHEN id = 1 THEN '' ELSE 'dir' || id END, depth, path::ltree,
		       now() - random() * interval '1000 days', 5 + (random() * 200)::int, 0
		FROM t`, id, *dirs, *fanout); err != nil {
		return err
	}

	// Files must not maintain secondary indexes while loading 10M rows. Partition
	// indexes cannot be dropped on their own, so the indexes of the partitioned
	// parent are saved, dropped and rebuilt afterwards (a throwaway database).
	rows, err := conn.Query(ctx, `SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'files' AND indexname NOT LIKE '%pkey'`)
	if err != nil {
		return err
	}
	type idx struct{ name, def string }
	var indexes []idx
	for rows.Next() {
		var x idx
		if err := rows.Scan(&x.name, &x.def); err != nil {
			return err
		}
		indexes = append(indexes, x)
	}
	rows.Close()
	for _, x := range indexes {
		if _, err := conn.Exec(ctx, `DROP INDEX `+x.name); err != nil {
			return err
		}
	}

	loadStart := time.Now()
	for from := int64(1); from <= *files; from += *chunk {
		to := min(from+*chunk-1, *files)
		if err := exec(fmt.Sprintf("insert files %d..%d", from, to), `
			INSERT INTO files (share_id, dir_id, name, ext, category_id, size, alloc, mtime, atime, ctime, nlink, counted)
			SELECT $1, d, 'f' || g || CASE WHEN e = '' THEN '' ELSE '.' || e END, e, coalesce(m.category_id, 8::smallint),
			       sz, ((sz + 4095) / 4096) * 4096, mt,
			       CASE WHEN r3 < 0.35 THEN mt ELSE mt + (now() - mt) * power(r3, 2) END, mt, 1, true
			FROM (
				SELECT s.g,
				       CASE WHEN s.r5 < 0.7 THEN 1 + floor($4 * random())::bigint ELSE 1 + floor($4 * power(random(), 4))::bigint END AS d,
				       CASE WHEN p.ext = 'dat' THEN 'x' || (s.g % 400) ELSE p.ext END AS e,
				       floor(power(10, p.min_exp + (p.max_exp - p.min_exp) * power(s.r4, 1.5)))::bigint AS sz,
				       now() - s.r2 * interval '2920 days' AS mt, s.r3
				FROM (SELECT g, random() AS r1, random() AS r2, random() AS r3, random() AS r4, random() AS r5
				      FROM generate_series($2::bigint, $3::bigint) AS g) s
				JOIN `+extPick+` ON s.r1 >= p.lo AND s.r1 < p.hi) x
			LEFT JOIN extension_map m ON m.ext = x.e`, id, from, to, *dirs); err != nil {
			return err
		}
	}
	fmt.Printf("loaded %d files in %.0fs (%.0f rows/s)\n", *files, time.Since(loadStart).Seconds(), float64(*files)/time.Since(loadStart).Seconds())

	// Hardlinks: every 400th file gets a second name in another directory.
	if err := exec("mark hardlink originals", `UPDATE files SET nlink = 2, dev = 1, ino = id WHERE share_id = $1 AND id % 400 = 0`, id); err != nil {
		return err
	}
	if err := exec("insert hardlink copies", `
		INSERT INTO files (share_id, dir_id, name, ext, category_id, size, alloc, mtime, atime, ctime, nlink, dev, ino, counted)
		SELECT share_id, 1 + (dir_id * 7) % $2, name || '.lnk', ext, category_id, size, alloc, mtime, atime, ctime, 2, dev, ino, true
		FROM files WHERE share_id = $1 AND id % 400 = 0 AND nlink = 2`, id, *dirs); err != nil {
		return err
	}

	for _, x := range indexes {
		if err := exec("create "+x.name, strings.Replace(x.def, " ON ONLY ", " ON ", 1)); err != nil {
			return err
		}
	}
	if err := exec("analyze", fmt.Sprintf("ANALYZE %s, %s", part("files"), part("dirs"))); err != nil {
		return err
	}

	t := time.Now()
	if err := st.RecomputeAll(ctx, id); err != nil {
		return fmt.Errorf("recompute: %w", err)
	}
	fmt.Printf("%-34s %8.1fs\n", "RecomputeAll (aggregates)", time.Since(t).Seconds())
	if err := exec("analyze aggregates", fmt.Sprintf("ANALYZE %s, %s, %s, %s", part("files"), part("dirs"), part("dir_category_agg"), part("share_ext_agg"))); err != nil {
		return err
	}

	// A completed scan so the share looks published.
	var scanID int64
	if err := conn.QueryRow(ctx, `
		INSERT INTO scans (share_id, status, mode, trigger, finished_at, dirs, files, bytes, alloc_bytes, atime_quality)
		SELECT $1, 'succeeded', 'full', 'bench', now(), 1 + dirs_total, files_total, size_total, alloc_total,
		       '{"verdict":"reliable","reasons":[],"sample_files":0,"pct_equal_mtime":0.35,"pct_before_mtime":0}'
		FROM dirs WHERE share_id = $1 AND parent_id IS NULL RETURNING id`, id).Scan(&scanID); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `UPDATE shares SET last_scan_id = $2 WHERE id = $1`, id, scanID); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `UPDATE dirs SET last_seen_scan = $2 WHERE share_id = $1`, id, scanID); err != nil {
		return err
	}

	var sizes []string
	for _, t := range []string{"files", "dirs", "dir_category_agg", "share_ext_agg"} {
		var tbl, tot string
		if err := conn.QueryRow(ctx, `SELECT $1::text, pg_size_pretty(pg_total_relation_size($1::regclass))`, part(t)).Scan(&tbl, &tot); err != nil {
			return err
		}
		sizes = append(sizes, fmt.Sprintf("%s=%s", t, tot))
	}
	fmt.Printf("share %q (id %d) ready: %s\n", *name, id, strings.Join(sizes, " "))
	return nil
}
