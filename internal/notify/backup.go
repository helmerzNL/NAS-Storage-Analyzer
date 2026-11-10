package notify

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Portable is a target as the in-app backup holds it: what the owner entered, and the address only as the sealed value the
// database already keeps (the key is not in the file). The outcome of the last delivery is not something the owner entered.
type Portable struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Lang    string `json:"lang"`
	Events  Events `json:"events"`
	Enabled bool   `json:"enabled"`
	Address []byte `json:"address"` // base64 in the file: the ciphertext of url_enc
}

// querier is the pool, or the transaction of a restore.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Export lists every target for a backup, by name. It never decrypts anything.
func (s *Service) Export(ctx context.Context) ([]Portable, error) { return export(ctx, s.pool) }

// Differs reports whether items (as Prepare returned them) are anything else than what is stored now. A file made on this
// installation and restored unchanged does not differ, so such a restore leaves the targets, and the outcome of their last
// delivery, alone.
func Differs(ctx context.Context, q querier, items []Portable) (bool, error) {
	cur, err := export(ctx, q)
	if err != nil {
		return false, err
	}
	if len(cur) != len(items) {
		return true, nil
	}
	sorted := append([]Portable(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name) })
	for i, p := range sorted {
		c := cur[i]
		if c.Name != p.Name || c.Format != p.Format || c.Lang != p.Lang || c.Events != p.Events || c.Enabled != p.Enabled || !bytes.Equal(c.Address, p.Address) {
			return true, nil
		}
	}
	return false, nil
}

func export(ctx context.Context, q querier) ([]Portable, error) {
	rows, err := q.Query(ctx, `SELECT name, format, lang, on_failed, on_empty, on_errors, on_finished, enabled, url_enc
		FROM notification_targets ORDER BY lower(name), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Portable{}
	for rows.Next() {
		var p Portable
		if err := rows.Scan(&p.Name, &p.Format, &p.Lang, &p.Events.Failed, &p.Events.Empty, &p.Events.Errors, &p.Events.Finished, &p.Enabled, &p.Address); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Prepare holds the targets of a backup file to the rules, changing nothing. It returns the ones that can be restored, and the
// names of the ones whose address does not open with this installation's key: those are left out, because a target without an
// address is nothing. An error is a *ValidationError whose field names the entry (notifications[2].format).
func (s *Service) Prepare(items []Portable) (ready []Portable, unreadable []string, err error) {
	seen := map[string]int{}
	ready = make([]Portable, 0, len(items))
	for i, p := range items {
		field := func(f string) string { return fmt.Sprintf("notifications[%d].%s", i, f) }
		in := Input{Name: p.Name, Format: p.Format, Lang: p.Lang, Events: p.Events, Enabled: p.Enabled}
		if cerr := in.clean(false); cerr != nil {
			ve := cerr.(*ValidationError)
			return nil, nil, &ValidationError{Field: field(ve.Field), Msg: ve.Msg}
		}
		key := strings.ToLower(in.Name)
		if j, dup := seen[key]; dup {
			return nil, nil, &ValidationError{Field: field("name"), Msg: fmt.Sprintf("the name %q is also in notifications[%d]", in.Name, j)}
		}
		seen[key] = i
		p.Name = in.Name
		plain, oerr := s.box.Open(p.Address)
		if oerr != nil {
			unreadable = append(unreadable, p.Name)
			continue
		}
		// What opens must still be an address this version would accept.
		if cerr := CheckURL(string(plain)); cerr != nil {
			return nil, nil, &ValidationError{Field: field("address"), Msg: "the stored address is not usable"}
		}
		ready = append(ready, p)
	}
	return ready, unreadable, nil
}

// ReplaceTx makes the stored targets equal to items (as Prepare returned them), inside the restore's transaction.
func (s *Service) ReplaceTx(ctx context.Context, tx pgx.Tx, items []Portable) error {
	if _, err := tx.Exec(ctx, `DELETE FROM notification_targets`); err != nil {
		return err
	}
	for _, p := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_targets (name, url_enc, format, lang, on_failed, on_empty, on_errors, on_finished, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.Name, p.Address, p.Format, p.Lang, p.Events.Failed, p.Events.Empty, p.Events.Errors, p.Events.Finished, p.Enabled); err != nil {
			return err
		}
	}
	return nil
}
