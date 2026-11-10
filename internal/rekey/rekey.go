// Package rekey replaces the key that encrypts what the database stores encrypted: the SMB passwords of shares and the
// authenticator secrets of users. It is the work behind `nasa rekey`.
package rekey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
)

// Column is one place where a sealed value is stored. Label is the column that names a row in a message (never the value), What
// says what one value is, and Counted what a number of them is.
type Column struct {
	Table, Column, Label, What, Counted string
}

// Columns lists every sealed column. A test compares it with the schema (every bytea column whose name ends in _enc), so a new
// encrypted column cannot be added without teaching this package about it.
var Columns = []Column{
	{Table: "shares", Column: "secret_enc", Label: "name", What: "the SMB password of share", Counted: "SMB password(s) of shares"},
	{Table: "users", Column: "totp_secret_enc", Label: "username", What: "the authenticator secret of user", Counted: "authenticator secret(s) of users"},
	{Table: "notification_targets", Column: "url_enc", Label: "name", What: "the address of notification target", Counted: "address(es) of notification targets"},
}

// tables is every table of Columns once, for the lock.
func tables() string {
	var names []string
	seen := map[string]bool{}
	for _, c := range Columns {
		if !seen[c.Table] {
			seen[c.Table] = true
			names = append(names, pgx.Identifier{c.Table}.Sanitize())
		}
	}
	return strings.Join(names, ", ")
}

// Result counts what was looked at, per column, in the order of Columns.
type Result struct {
	Counts []int
}

// String says what was counted: "1 SMB password(s) of shares, 0 authenticator secret(s) of users, ...".
func (r Result) String() string {
	var parts []string
	for i, c := range Columns {
		n := 0
		if i < len(r.Counts) {
			n = r.Counts[i]
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, c.Counted))
	}
	return strings.Join(parts, ", ")
}

// Total is the number of stored secrets.
func (r Result) Total() int {
	n := 0
	for _, c := range r.Counts {
		n += c
	}
	return n
}

// ErrUnreadable is returned when a stored value opens with none of the keys given. Nothing has been changed then.
var ErrUnreadable = errors.New("some stored secrets cannot be opened")

type row struct {
	id    int64
	label string
	value []byte
}

func load(ctx context.Context, q pgx.Tx, c Column, lock bool) ([]row, error) {
	sql := fmt.Sprintf(`SELECT id, %s, %s FROM %s WHERE %s IS NOT NULL AND octet_length(%s) > 0 ORDER BY id`,
		pgx.Identifier{c.Label}.Sanitize(), pgx.Identifier{c.Column}.Sanitize(), pgx.Identifier{c.Table}.Sanitize(),
		pgx.Identifier{c.Column}.Sanitize(), pgx.Identifier{c.Column}.Sanitize())
	if lock {
		sql += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.label, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Check reports whether every stored secret opens with the key. problems names the ones that do not (by share or user, never by
// value); it is empty when all is well. Nothing is changed.
func Check(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box) (res Result, problems []string, err error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, nil, err
	}
	defer tx.Rollback(ctx)
	for _, c := range Columns {
		rows, err := load(ctx, tx, c, false)
		if err != nil {
			return res, nil, err
		}
		res.Counts = append(res.Counts, len(rows))
		for _, r := range rows {
			if _, err := box.Open(r.value); err != nil {
				problems = append(problems, fmt.Sprintf("%s %q", c.What, r.label))
			}
		}
	}
	return res, problems, nil
}

// Run seals every stored secret with the new key, in one transaction: either all of them are under the new key afterwards, or
// nothing has changed. A value that opens with neither key stops everything (ErrUnreadable, with the rows named in the error).
// A database that is already under the new key is left as it is and reported as done (already is true), so running the command
// twice does no harm. The rows are locked while it runs, so a running app cannot write a value under the old key in between; it
// must still be restarted with the new key afterwards.
func Run(ctx context.Context, pool *pgxpool.Pool, oldBox, newBox *secrets.Box, actor string) (res Result, already bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, false, err
	}
	defer tx.Rollback(ctx)
	// Writers of these tables wait until the new values are in: nothing can be sealed with the old key behind our back.
	if _, err := tx.Exec(ctx, `LOCK TABLE `+tables()+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return res, false, err
	}
	var unreadable []string
	changed, underNew := 0, 0
	for _, c := range Columns {
		rows, err := load(ctx, tx, c, true)
		if err != nil {
			return res, false, err
		}
		res.Counts = append(res.Counts, len(rows))
		update := fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE id = $1`, pgx.Identifier{c.Table}.Sanitize(), pgx.Identifier{c.Column}.Sanitize())
		for _, r := range rows {
			plain, err := oldBox.Open(r.value)
			if err != nil {
				if _, nerr := newBox.Open(r.value); nerr == nil {
					underNew++
					continue
				}
				unreadable = append(unreadable, fmt.Sprintf("%s %q", c.What, r.label))
				continue
			}
			if _, err := tx.Exec(ctx, update, r.id, newBox.Seal(plain)); err != nil {
				return res, false, err
			}
			changed++
		}
	}
	if len(unreadable) > 0 {
		return res, false, fmt.Errorf("%w with the current key or the new one: %s. Nothing was changed", ErrUnreadable, strings.Join(unreadable, "; "))
	}
	if changed == 0 && underNew > 0 {
		return res, true, nil // already done: the transaction ends without a change
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (user_id, username, ip, action, target, detail) VALUES (NULL, $1, '', 'encryption.rekey', '', $2)`,
		actor, map[string]any{"secrets": changed}); err != nil {
		return res, false, err
	}
	return res, false, tx.Commit(ctx)
}
