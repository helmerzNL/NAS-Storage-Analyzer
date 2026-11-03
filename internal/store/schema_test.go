package store_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// The schema guard (baseline section 2). A migration is only ever exercised by an upgrade, and a
// migration that is edited after it shipped makes a fresh install differ from an upgraded one with
// nothing failing. So the schema after every migration is recorded in testdata/schema and compared
// on every run, and rows written at version 1 are carried through every migration.
//
// A snapshot of a migration that has shipped is never edited: a change in an existing snapshot in a
// pull request is the thing to question. A new migration comes with its snapshot:
//
//	go test ./internal/store -run TestSchemaSnapshots -args -write-new-snapshots

var writeNew = flag.Bool("write-new-snapshots", false, "write the schema snapshot of a migration that has none yet")

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), ts.NewEmptyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// dumpSchema describes the public schema from the catalogs, in a fixed order. It reads
// pg_catalog rather than running pg_dump so that it needs no client tool and carries no version
// noise. Objects owned by an extension (the ltree and pg_trgm functions) are left out; the
// extensions themselves are listed.
func dumpSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	lines := func(query string, args ...any) []string {
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = fmt.Sprint(v)
			}
			out = append(out, strings.Join(parts, " | "))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	emit := func(prefix string, ls []string) {
		for _, l := range ls {
			b.WriteString(prefix + strings.TrimRight(l, " \t") + "\n")
		}
	}

	emit("extension ", lines(`SELECT extname FROM pg_extension ORDER BY 1`))
	emit("sequence ", lines(`SELECT sequencename, data_type::text, start_value, min_value, max_value, increment_by, cycle, cache_size
		FROM pg_sequences WHERE schemaname = 'public' ORDER BY 1`))

	rows, err := pool.Query(ctx, `SELECT c.oid, c.relname, coalesce(pg_get_partkeydef(c.oid), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	type tbl struct {
		oid       uint32
		name, key string
	}
	var tables []tbl
	for rows.Next() {
		var x tbl
		if err := rows.Scan(&x.oid, &x.name, &x.key); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, x)
	}
	rows.Close()
	for _, x := range tables {
		fmt.Fprintf(&b, "table %s", x.name)
		if x.key != "" {
			fmt.Fprintf(&b, " partitioned by %s", x.key)
		}
		b.WriteString("\n")
		emit("  column ", lines(`SELECT a.attname, format_type(a.atttypid, a.atttypmod),
				CASE WHEN a.attnotnull THEN 'NOT NULL' ELSE 'NULL' END, coalesce(pg_get_expr(d.adbin, d.adrelid), ''),
				a.attidentity::text, a.attgenerated::text
			FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, x.oid))
		emit("  constraint ", lines(`SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1 ORDER BY conname`, x.oid))
		emit("  index ", lines(`SELECT pg_get_indexdef(indexrelid) FROM pg_index WHERE indrelid = $1 ORDER BY 1`, x.oid))
		emit("  trigger ", lines(`SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid = $1 AND NOT tgisinternal ORDER BY 1`, x.oid))
	}
	emit("view ", lines(`SELECT viewname, replace(definition, E'\n', ' ') FROM pg_views WHERE schemaname = 'public' ORDER BY 1`))
	for _, def := range lines(`SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.prokind = 'f'
		AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
		ORDER BY p.proname, p.oid`) {
		b.WriteString("function\n")
		emit("  ", strings.Split(strings.TrimSpace(def), "\n"))
	}
	return b.String()
}

func snapshotPath(version int) string {
	return filepath.Join("testdata", "schema", fmt.Sprintf("%04d.txt", version))
}

func header(version int) string {
	return fmt.Sprintf("# Schema of a database after migration %04d, from pg_catalog (PostgreSQL 16).\n"+
		"# Written by TestSchemaSnapshots; never edited once the migration has shipped.\n", version)
}

// difference lists the lines that are in only one of the two texts.
func difference(want, got string) string {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[l]++
		}
		return m
	}
	w, g := count(want), count(got)
	var out []string
	for l, n := range w {
		if g[l] < n {
			out = append(out, "  only in the snapshot: "+l)
		}
	}
	for l, n := range g {
		if w[l] < n {
			out = append(out, "  only in the replay:   "+l)
		}
	}
	sort.Strings(out)
	if len(out) > 20 {
		out = append(out[:20], fmt.Sprintf("  ... and %d more", len(out)-20))
	}
	return strings.Join(out, "\n")
}

func TestMigrationVersionsAreContiguous(t *testing.T) {
	versions, err := store.MigrationVersions()
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range versions {
		if v != i+1 {
			t.Fatalf("migration versions are %v: they must be 1, 2, 3, ... without gaps or repeats", versions)
		}
	}
}

// TestSchemaSnapshots replays the migrations on an empty database, one version at a time, and compares
// the schema after each with the snapshot taken when that migration was written. A shipped migration
// that was edited since, a migration without a snapshot and a snapshot without a migration all fail.
func TestSchemaSnapshots(t *testing.T) {
	versions, err := store.MigrationVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		t.Run(fmt.Sprintf("%04d", v), func(t *testing.T) {
			st := open(t)
			if err := st.MigrateTo(context.Background(), v); err != nil {
				t.Fatal(err)
			}
			got := header(v) + dumpSchema(t, st.Pool)
			want, err := os.ReadFile(snapshotPath(v))
			switch {
			case os.IsNotExist(err) && *writeNew:
				if err := os.WriteFile(snapshotPath(v), []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s: review it and commit it", snapshotPath(v))
			case os.IsNotExist(err):
				t.Fatalf("migration %04d has no schema snapshot. Fix: go test ./internal/store -run TestSchemaSnapshots -args -write-new-snapshots, then review and commit %s", v, snapshotPath(v))
			case err != nil:
				t.Fatal(err)
			case string(want) != got:
				t.Fatalf("the schema after migration %04d is not the one recorded when it shipped.\n%s\n"+
					"Fix: a shipped migration is never edited; put the change in a new migration. (If this migration has not shipped, "+
					"delete its snapshot and regenerate it with -args -write-new-snapshots.)", v, difference(string(want), got))
			}
		})
	}
	t.Run("no snapshot without a migration", func(t *testing.T) {
		files, err := filepath.Glob(filepath.Join("testdata", "schema", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != len(versions) {
			t.Fatalf("%d snapshots for %d migrations: %v", len(files), len(versions), files)
		}
	})
}

// TestUpgradeKeepsExistingRows stands a database at version 1 with a share and a scan, moves it forward
// one migration at a time, and checks that the rows survive and end up with the same values as a share and
// a scan created on a database that never was at version 1. A migration that adds a column with a default
// fills the old rows with it; one that backfills differently from the column's default shows up here.
func TestUpgradeKeepsExistingRows(t *testing.T) {
	ctx := context.Background()
	versions, err := store.MigrationVersions()
	if err != nil {
		t.Fatal(err)
	}
	// Only the columns that existed at version 1: this is what an old installation holds.
	const insertShare = `INSERT INTO shares (name, kind) VALUES ($1, 'bind') RETURNING id`
	const insertScan = `INSERT INTO scans (share_id, mode) VALUES ($1, 'full')`

	old := open(t)
	if err := old.MigrateTo(ctx, versions[0]); err != nil {
		t.Fatal(err)
	}
	var oldShare int64
	if err := old.Pool.QueryRow(ctx, insertShare, "kept").Scan(&oldShare); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Pool.Exec(ctx, insertScan, oldShare); err != nil {
		t.Fatal(err)
	}
	for _, v := range versions[1:] {
		if err := old.MigrateTo(ctx, v); err != nil {
			t.Fatalf("upgrading to %04d with existing rows: %v", v, err)
		}
		var shares, scans int
		if err := old.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM shares), (SELECT count(*) FROM scans)`).Scan(&shares, &scans); err != nil {
			t.Fatal(err)
		}
		if shares != 1 || scans != 1 {
			t.Fatalf("after migration %04d there are %d shares and %d scans, want 1 and 1", v, shares, scans)
		}
	}

	fresh := open(t)
	if err := fresh.MigrateTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var freshShare int64
	if err := fresh.Pool.QueryRow(ctx, insertShare, "fresh").Scan(&freshShare); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Pool.Exec(ctx, insertScan, freshShare); err != nil {
		t.Fatal(err)
	}

	row := func(st *store.Store, query string) string {
		var s string
		if err := st.Pool.QueryRow(ctx, query).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for name, query := range map[string]string{
		"shares": `SELECT (to_jsonb(s) - 'id' - 'name' - 'created_at')::text FROM shares s`,
		"scans":  `SELECT (to_jsonb(c) - 'id' - 'share_id' - 'started_at')::text FROM scans c`,
	} {
		if a, b := row(old, query), row(fresh, query); a != b {
			t.Errorf("%s: a row carried up from version 1 is\n  %s\nbut a new row on a fresh install is\n  %s", name, a, b)
		}
	}
}
