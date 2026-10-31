package reset_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
	"github.com/helmerznl/nas-storage-analyzer/internal/reset"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

func exec(t *testing.T, st *store.Store, sql string, args ...any) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	return n
}

// fill puts at least one row in every table of the schema, the way a long-used installation has: accounts and everything that unlocks them,
// shares with scan data and history, work in progress, the audit log, and what the backup feature keeps.
func fill(t *testing.T, st *store.Store) {
	t.Helper()
	exec(t, st, `INSERT INTO users (username, role, password_hash, prefs) VALUES ('helmer', 'admin', '$argon2id$x', '{"theme":"dark"}'), ('anna', 'viewer', NULL, '{}')`)
	exec(t, st, `INSERT INTO recovery_codes (user_id, code_hash) SELECT id, '\x01' FROM users`)
	exec(t, st, `INSERT INTO webauthn_credentials (credential_id, user_id, name, public_key) SELECT '\x0102', id, 'laptop', '\x03' FROM users WHERE username = 'helmer'`)
	exec(t, st, `INSERT INTO sessions (token_hash, user_id, stage, csrf_token, idle_expires_at, absolute_expires_at)
		SELECT '\x04', id, 'active', 'csrf', now() + interval '1 day', now() + interval '1 day' FROM users WHERE username = 'helmer'`)
	exec(t, st, `INSERT INTO webauthn_ceremonies (id, user_id, kind, data, expires_at) SELECT 'c1', id, 'reauth', '{}', now() + interval '5 minutes' FROM users WHERE username = 'helmer'`)
	exec(t, st, `INSERT INTO login_failures (scope, key) VALUES ('user', 'helmer')`)
	exec(t, st, `INSERT INTO audit_log (action, username) VALUES ('login.success', 'helmer')`)
	exec(t, st, `INSERT INTO app_settings (key, value) VALUES ('k', '"v"')`)
	exec(t, st, `INSERT INTO invitations (token_hash, kind, username, role, expires_at) VALUES ('\x05', 'new', 'guest', 'viewer', now() + interval '1 day')`)

	exec(t, st, `INSERT INTO shares (name, kind, config, secret_enc) VALUES ('media', 'bind', '{"path":"/mnt/shares/media"}', '\x06'), ('docs', 'bind', '{"path":"/mnt/shares/docs"}', NULL)`)
	exec(t, st, `UPDATE extension_map SET category_id = 5 WHERE ext = 'mkv'`) // edited by the owner
	exec(t, st, `INSERT INTO extension_map (ext, category_id) VALUES ('zzz', 1)`)
	for _, id := range []int{1, 2} {
		exec(t, st, `SELECT ensure_share_partitions($1)`, id)
		exec(t, st, `INSERT INTO scans (id, share_id, status, mode) VALUES ($1, $2, 'succeeded', 'full')`, 100+id, id)
		exec(t, st, `INSERT INTO scan_errors (scan_id, path, kind, message, at) VALUES ($1, '/x', 'read', 'denied', now())`, 100+id)
		exec(t, st, `INSERT INTO dirs (share_id, id, parent_id, name, depth, path, mtime, entry_count, last_seen_scan) VALUES ($1, nextval('dir_id_seq'), NULL, '', 0, '', now(), 1, $2)`, id, 100+id)
		exec(t, st, `INSERT INTO files (share_id, dir_id, name, ext, category_id, size, alloc, mtime, atime, ctime) VALUES ($1, 1, 'a.mkv', 'mkv', 1, 10, 10, now(), now(), now())`, id)
		exec(t, st, `INSERT INTO dir_category_agg (share_id, dir_id, category_id) VALUES ($1, 1, 1)`, id)
		exec(t, st, `INSERT INTO share_ext_agg (share_id, ext, size, alloc, files) VALUES ($1, 'mkv', 10, 10, 1)`, id)
		exec(t, st, `INSERT INTO jobs (kind, share_id, state, finished_at) VALUES ('scan', $1, 'done', now())`, id)
		exec(t, st, `INSERT INTO snapshots (scan_id, share_id, taken_at, size, alloc, files, dirs) VALUES ($1, $2, now(), 10, 10, 1, 1)`, 100+id, id)
		exec(t, st, `INSERT INTO snapshot_category (scan_id, category_id, size, files) VALUES ($1, 1, 10, 1)`, 100+id)
		exec(t, st, `INSERT INTO snapshot_ext (scan_id, ext, size, files) VALUES ($1, 'mkv', 10, 1)`, 100+id)
		exec(t, st, `INSERT INTO snapshot_dir (scan_id, dir_id, path, size, files) VALUES ($1, 1, '', 10, 1)`, 100+id)
	}
	exec(t, st, `INSERT INTO backup_stages (token, created_by, size, data) VALUES (repeat('a', 32), 'helmer', 1, '\x07')`)
	exec(t, st, `INSERT INTO backup_jobs (started_by) VALUES ('helmer')`)
}

func tablesOf(t *testing.T, st *store.Store) []string {
	t.Helper()
	names, err := reset.Tables(context.Background(), st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestAResetEmptiesEveryTableAndPutsTheSeedBack(t *testing.T) {
	st := ts.NewStore(t)
	fill(t, st)
	tables := tablesOf(t, st)
	seeded := map[string]int{"categories": len(categorize.Categories), "extension_map": len(categorize.DefaultExtensions())}
	migrations := count(t, st, "schema_migrations")

	// The test fills every table: a table a later migration adds fails here until this test fills it, and so is known to be wiped.
	for _, tbl := range tables {
		if count(t, st, tbl) == 0 {
			t.Errorf("the test does not put a row in %q: a reset would not be shown to empty it", tbl)
		}
	}
	if len(tables) < 20 {
		t.Fatalf("only %d tables found", len(tables))
	}

	res, err := reset.New(st.Pool, nil).Wipe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		want := seeded[tbl] // the built-in categories and the default file types come back; everything else is empty
		if got := count(t, st, tbl); got != want {
			t.Errorf("%s has %d rows after a reset, want %d", tbl, got, want)
		}
	}
	if count(t, st, "schema_migrations") != migrations {
		t.Error("the record of applied migrations was emptied: the schema would be created over itself")
	}
	if res.Rows < 20 || res.Shares != 2 || res.Tables["users"] != 2 || res.Tables["audit_log"] != 1 {
		t.Errorf("the result (for the log): %+v", res)
	}

	// The file types the owner edited are the defaults again.
	var cat int16
	if err := st.Pool.QueryRow(context.Background(), `SELECT category_id FROM extension_map WHERE ext = 'mkv'`).Scan(&cat); err != nil || cat != categorize.Video {
		t.Errorf("an edited file type is still edited: %d %v", cat, err)
	}
	if n := count(t, st, `extension_map WHERE ext = 'zzz'`); n != 0 {
		t.Error("a file type the owner added is still there")
	}
	// It starts like a new installation: no scan data left behind, and ids start at 1.
	if n := count(t, st, `pg_tables WHERE schemaname = 'public' AND (tablename LIKE 'dirs\_s%' OR tablename LIKE 'files\_s%')`); n != 0 {
		t.Errorf("%d partitions of removed shares are still there", n)
	}
	exec(t, st, `INSERT INTO shares (name, kind, config) VALUES ('fresh', 'bind', '{}')`)
	var id, dir int64
	st.Pool.QueryRow(context.Background(), `SELECT id FROM shares`).Scan(&id)
	st.Pool.QueryRow(context.Background(), `SELECT nextval('dir_id_seq')`).Scan(&dir)
	if id != 1 || dir != 1 {
		t.Errorf("ids did not restart: share %d, directory %d", id, dir)
	}
	exec(t, st, `SELECT ensure_share_partitions(1)`) // a new share can be given its partitions again
}

func TestAFailureHalfwayWipesNothing(t *testing.T) {
	for _, step := range []string{"counted", "partitions", "truncated", "seeded"} {
		t.Run("after "+step, func(t *testing.T) {
			st := ts.NewStore(t)
			fill(t, st)
			before := map[string]int{}
			for _, tbl := range tablesOf(t, st) {
				before[tbl] = count(t, st, tbl)
			}
			s := reset.New(st.Pool, nil)
			s.SetHookForTest(func(name string) error {
				if name == step {
					return errors.New("the database went away")
				}
				return nil
			})
			if _, err := s.Wipe(context.Background()); err == nil {
				t.Fatal("the wipe did not fail")
			}
			for tbl, n := range before {
				if got := count(t, st, tbl); got != n {
					t.Errorf("%s: %d rows after a failed reset, had %d", tbl, got, n)
				}
			}
			if n := count(t, st, `pg_tables WHERE schemaname = 'public' AND tablename = 'dirs_s1'`); n != 1 {
				t.Error("the scan data of a share was dropped by a reset that failed")
			}
		})
	}
}

func TestAResetWaitsForScans(t *testing.T) {
	for _, state := range []string{"queued", "running"} {
		t.Run(state, func(t *testing.T) {
			st := ts.NewStore(t)
			fill(t, st)
			exec(t, st, fmt.Sprintf(`INSERT INTO jobs (kind, share_id, state) VALUES ('recategorize', 1, '%s')`, state))
			s := reset.New(st.Pool, nil)
			if busy, err := s.Busy(context.Background()); err != nil || !busy {
				t.Fatalf("Busy: %v %v", busy, err)
			}
			if _, err := s.Wipe(context.Background()); !errors.Is(err, reset.ErrBusy) {
				t.Fatalf("a reset ran under a %s job: %v", state, err)
			}
			if count(t, st, "users") != 2 || count(t, st, "shares") != 2 {
				t.Error("a reset that waited for scans wiped something")
			}
		})
	}
}

func TestAnEmptyInstallationCanBeResetToo(t *testing.T) {
	st := ts.NewStore(t)
	if _, err := reset.New(st.Pool, nil).Wipe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, st, "categories") != len(categorize.Categories) {
		t.Error("the seed is missing")
	}
}
