package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

// runScript measures the lookup behind POST /shares/{share}/delete-script: 2000 file ids resolved to paths, with the
// check for names that differ only in case. The endpoint is admin-only and is not part of the GET scenarios of
// "queries", so the query is called directly, the way the handler calls it.
func runScript(args []string) error {
	fs := flag.NewFlagSet("script", flag.ExitOnError)
	dsn := fs.String("db", os.Getenv("NASA_DB_DSN"), "PostgreSQL connection string")
	name := fs.String("share", "bench", "share to measure")
	n := fs.Int("n", 2000, "files per request")
	runs := fs.Int("runs", 20, "warm runs per scenario")
	fs.Parse(args)
	if *dsn == "" {
		return errors.New("pass -db or set NASA_DB_DSN")
	}
	ctx := context.Background()
	pool, err := store.OpenReadOnly(ctx, *dsn, 5, 60*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	var share, files int64
	if err := pool.QueryRow(ctx, `SELECT s.id, d.files_total FROM shares s JOIN dirs d ON d.share_id = s.id AND d.parent_id IS NULL
		WHERE s.name = $1`, *name).Scan(&share, &files); err != nil {
		return fmt.Errorf("share %q: %w", *name, err)
	}
	ids := func(sql string, a ...any) []int64 {
		rows, err := pool.Query(ctx, sql, a...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			out = append(out, id)
		}
		return out
	}
	var widest, widestFiles int64
	pool.QueryRow(ctx, `SELECT id, files_direct FROM dirs WHERE share_id = $1 ORDER BY files_direct DESC LIMIT 1`, share).Scan(&widest, &widestFiles)

	scenarios := []struct {
		name string
		ids  []int64
	}{
		{"spread over the share (every file at a fixed stride)", ids(`SELECT id FROM files WHERE share_id = $1 AND id % GREATEST($2::bigint, 1) = 0 ORDER BY id LIMIT $3`,
			share, files/int64(*n)/2, *n)},
		{fmt.Sprintf("all in the widest folder (%s files in it)", sep(widestFiles)), ids(`SELECT id FROM files WHERE share_id = $1 AND dir_id = $2 ORDER BY id LIMIT $3`, share, widest, *n)},
		{"the 2000 largest files", ids(`SELECT id FROM files WHERE share_id = $1 ORDER BY size DESC LIMIT $2`, share, *n)},
	}
	q := query.New(pool)
	fmt.Printf("%s files in the share; %d files per request, %d runs\n\n", sep(files), *n, *runs)
	fmt.Printf("%-62s %8s %8s %8s %8s %6s\n", "scenario", "first", "p50", "p95", "max", "twins")
	for _, s := range scenarios {
		var times []time.Duration
		twins := 0
		for i := 0; i <= *runs; i++ {
			t0 := time.Now()
			sel, err := q.FilesForScript(ctx, share, s.ids)
			d := time.Since(t0)
			if err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
			twins = 0
			for _, f := range sel.Files {
				if f.Twin {
					twins++
				}
			}
			times = append(times, d)
		}
		first, warm := times[0], times[1:]
		sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
		fmt.Printf("%-62s %6.0f ms %6.0f ms %6.0f ms %6.0f ms %6d\n", s.name, ms(first), ms(warm[len(warm)/2]),
			ms(warm[min(len(warm)-1, len(warm)*95/100)]), ms(warm[len(warm)-1]), twins)
	}
	return nil
}
