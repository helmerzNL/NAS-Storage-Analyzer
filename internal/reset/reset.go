// Package reset puts the installation back to "new" on purpose, following App-Guidance's factory-reset.md. It is the one feature that destroys
// everything, so almost all of the design is about making sure the administrator asked and knows what it costs (that is in internal/api and
// internal/auth: the typed word and the fresh proof of who is asking). What is left here is the wipe itself.
//
// What it deletes is **every table of the schema**, found by asking the database and not from a list: a table added by a later migration is
// emptied without anyone remembering to add it, and a test fails if it is not. Only the record of which migrations have run is kept, and the
// built-in categories and the default file types are put back, so the application starts the setup wizard again exactly as on a first install.
//
// What it does not touch lives outside the database: the environment variables (including NASA_ENCRYPTION_KEY, which is why a restart is
// not needed to "forget" it: it was never stored), the image, the host folder DATA_PATH itself, and the files on the NAS, which the scanner
// only ever reads.
package reset

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

// Phrase is the word the administrator types. It stops an accidental click and a script that replays an old request. It is defined here and
// nowhere else: the interface asks the server for it.
const Phrase = "RESET"

// ErrBusy: scans are queued or running. Wiping the tables under a scan would make the scan fail in confusing ways, so a reset waits until
// they are done or cancelled, like deleting a share does.
var ErrBusy = errors.New("a scan is queued or running; wait until it is done or cancel it, then reset")

// keep is the one table that is not emptied: which migrations have been applied. Without it the schema would be recreated over itself.
const keep = "schema_migrations"

// Service wipes the installation.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	// hook runs between the steps, inside the transaction; a test makes it fail to prove that a failure halfway wipes nothing.
	hook func(step string) error
}

// New builds the service on a read-write pool.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{pool: pool, log: log}
}

// Result is what was removed, as counts and names of tables: never contents.
type Result struct {
	Tables map[string]int64 // rows that were in each emptied table
	Rows   int64
	Shares int // shares whose scan data (partitions) were dropped
}

// Tables lists the tables a reset empties: every table of the schema except the migration record, and without the partitions, which belong to
// their parent. It is also what the tests check against.
func Tables(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND c.relname <> $1 ORDER BY c.relname`, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Wipe empties every table in one transaction and puts the seed back. Nothing is wiped if anything fails. The caller has already established
// who is asking; this refuses only while scans are queued or running.
func (s *Service) Wipe(ctx context.Context) (*Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	// A long export holds the tables: fail after a while instead of stalling the whole application behind the lock. Nothing is wiped then.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return nil, err
	}
	// Keeps a scan from being queued between the check below and the wipe (a job takes a share-row lock).
	if _, err := tx.Exec(ctx, `LOCK TABLE shares IN EXCLUSIVE MODE`); err != nil {
		return nil, err
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE state IN ('queued', 'running'))`).Scan(&busy); err != nil {
		return nil, err
	}
	if busy {
		return nil, ErrBusy
	}

	tables, err := Tables(ctx, tx)
	if err != nil {
		return nil, err
	}
	res := &Result{Tables: make(map[string]int64, len(tables))}
	for _, t := range tables {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{t}.Sanitize()).Scan(&n); err != nil {
			return nil, err
		}
		res.Tables[t] = n
		res.Rows += n
	}
	if err := s.step("counted"); err != nil {
		return nil, err
	}

	// The scan data of each share lives in partitions of its own: drop them, so a new share 1 starts with new tables and no old name is left.
	rows, err := tx.Query(ctx, `SELECT id FROM shares ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `SELECT drop_share_partitions($1)`, id); err != nil {
			return nil, err
		}
	}
	res.Shares = len(ids)
	if err := s.step("partitions"); err != nil {
		return nil, err
	}

	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = pgx.Identifier{t}.Sanitize()
	}
	if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(quoted, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		return nil, err
	}
	// Sequences that no column owns (the ids of directories and files) are not restarted by RESTART IDENTITY.
	seq, err := tx.Query(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind = 'S'`)
	if err != nil {
		return nil, err
	}
	var seqs []string
	for seq.Next() {
		var n string
		if err := seq.Scan(&n); err != nil {
			seq.Close()
			return nil, err
		}
		seqs = append(seqs, n)
	}
	seq.Close()
	if err := seq.Err(); err != nil {
		return nil, err
	}
	for _, n := range seqs {
		if _, err := tx.Exec(ctx, `ALTER SEQUENCE `+pgx.Identifier{n}.Sanitize()+` RESTART`); err != nil {
			return nil, err
		}
	}
	if err := s.step("truncated"); err != nil {
		return nil, err
	}

	if err := store.SeedTx(ctx, tx); err != nil {
		return nil, err
	}
	if err := s.step("seeded"); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) step(name string) error {
	if s.hook != nil {
		return s.hook(name)
	}
	return nil
}

// Busy reports whether scans are queued or running, for a cheap early refusal before anything else is spent on the request.
func (s *Service) Busy(ctx context.Context) (bool, error) {
	var busy bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE state IN ('queued', 'running'))`).Scan(&busy)
	return busy, err
}

// LogResult writes the one record a reset leaves: the process log, because the audit log goes with the database. It names tables and counts,
// never contents, and never a key or a token.
func (s *Service) LogResult(by, ip string, r *Result) {
	removed := make([]any, 0, 2*len(r.Tables))
	for t, n := range r.Tables {
		if n > 0 {
			removed = append(removed, t, n)
		}
	}
	args := append([]any{"by", by, "ip", ip, "tables", len(r.Tables), "rows", r.Rows, "shares", r.Shares}, removed...)
	s.log.Warn("factory reset: every table of the database was emptied", args...)
}
