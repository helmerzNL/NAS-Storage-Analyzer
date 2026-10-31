// Package store holds all PostgreSQL access: migrations, seed data, scan
// bookkeeping and the bulk writer that publishes a scan atomically.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store wraps the connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// migrateLockKey serialises concurrent migrators (app and worker starting together).
const migrateLockKey int64 = 0x4e41534d // "NASM"

// Migrate applies all embedded migrations that have not run yet, then seeds
// categories and default extensions.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockKey)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version int PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		ver, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: name must start with a number", name)
		}
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, ver).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, ver, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return s.seed(ctx)
}

// seed inserts the built-in categories and default extension mapping. Rows
// already present (including ones edited through the UI) are left alone.
func (s *Store) seed(ctx context.Context) error {
	b := &pgx.Batch{}
	for _, c := range categorize.Categories {
		b.Queue(`INSERT INTO categories (id, key) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET key = EXCLUDED.key`, c.ID, c.Key)
	}
	for ext, cat := range categorize.DefaultExtensions() {
		b.Queue(`INSERT INTO extension_map (ext, category_id) VALUES ($1, $2) ON CONFLICT (ext) DO NOTHING`, ext, cat)
	}
	res := s.Pool.SendBatch(ctx, b)
	defer res.Close()
	for i := 0; i < b.Len(); i++ {
		if _, err := res.Exec(); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return nil
}

// LoadMapper builds the extension mapper from the extension_map table.
func (s *Store) LoadMapper(ctx context.Context) (*categorize.Mapper, error) {
	rows, err := s.Pool.Query(ctx, `SELECT ext, category_id FROM extension_map`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int16{}
	for rows.Next() {
		var ext string
		var cat int16
		if err := rows.Scan(&ext, &cat); err != nil {
			return nil, err
		}
		m[ext] = cat
	}
	return categorize.NewMapper(m), rows.Err()
}

// ExcludesHash fingerprints an exclude list; a change forces a full scan
// because reused directories would otherwise keep rows that are now excluded.
func ExcludesHash(patterns []string) string {
	p := append([]string(nil), patterns...)
	sort.Strings(p)
	sum := sha256.Sum256([]byte(strings.Join(p, "\x00")))
	return hex.EncodeToString(sum[:8])
}
