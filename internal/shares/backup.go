package shares

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
)

// What a backup says about shares and file types (App-Guidance backup-restore.md). The orchestration (the file, the checks of the file as a
// whole, the job) lives in internal/backup; what is specific to a share lives here, next to the validator and the SQL that already know it.

var extRE = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// ValidExt reports whether s is an extension the file-type map accepts: 1-16 lower-case letters or digits, without a dot.
func ValidExt(s string) bool { return extRE.MatchString(s) }

// Portable is a share as a backup file holds it: the settings, and the SMB password only as the encrypted blob the database holds. The plain
// password is never written; "password" is part of Input (it is what an administrator types in the form), so a file that carries one is
// refused by Prepare rather than ignored.
type Portable struct {
	Input
	Secret []byte `json:"secret,omitempty"` // base64 in the file: the ciphertext of secret_enc, sealed with the key in the environment
}

// Export lists every share for a backup, by name. It never decrypts anything.
func (s *Service) Export(ctx context.Context) ([]Portable, error) {
	rows, err := s.pool.Query(ctx, loadSQL+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Portable{}
	for rows.Next() {
		f, sec, err := scanFull(rows)
		if err != nil {
			return nil, err
		}
		in := f.Input
		in.Password = nil
		if in.Kind != KindSMB {
			sec = nil
		}
		out = append(out, Portable{Input: in, Secret: sec})
	}
	return out, rows.Err()
}

// ItemError says which entry of a backup file is wrong and why: Field is for example "shares[2].cron".
type ItemError = ValidationError

// Prepare holds the shares of a backup file to the rules a share typed into the form is held to (Validate), and to the rules only a whole
// file can break: a name twice, a plain password, a secret on a share that has none. It returns the shares as they will be stored, and the
// names of the shares whose SMB password could not be read with this installation's key: those are restored without it (a blob that cannot be
// opened is not stored as if it were a password) and the owner is told which.
func (s *Service) Prepare(items []Portable) (ready []Portable, unreadable []string, err error) {
	seen := make(map[string]int, len(items))
	ready = make([]Portable, 0, len(items))
	for i, p := range items {
		field := func(f string) string { return fmt.Sprintf("shares[%d].%s", i, f) }
		if p.Password != nil {
			return nil, nil, &ItemError{Field: field("password"), Msg: "the file holds a password in plain text; a backup never does, so it is refused"}
		}
		if err := Validate(&p.Input, s.opt); err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				return nil, nil, &ItemError{Field: field(ve.Field), Msg: ve.Msg + fmt.Sprintf(" (share %q)", p.Name)}
			}
			return nil, nil, err
		}
		if j, dup := seen[p.Name]; dup {
			return nil, nil, &ItemError{Field: field("name"), Msg: fmt.Sprintf("the name %q is also used by shares[%d]", p.Name, j)}
		}
		seen[p.Name] = i
		if len(p.Secret) > 0 {
			if p.Kind != KindSMB {
				return nil, nil, &ItemError{Field: field("secret"), Msg: fmt.Sprintf("only an SMB share has a password (share %q)", p.Name)}
			}
			if len(p.Secret) > 4096 {
				return nil, nil, &ItemError{Field: field("secret"), Msg: "too long to be a password"}
			}
			if _, err := s.box.Open(p.Secret); err != nil {
				p.Secret = nil
				unreadable = append(unreadable, p.Name)
			}
		}
		ready = append(ready, p)
	}
	return ready, unreadable, nil
}

// Plan is what a restore will do to the shares that exist now. A share that is in the file and already has exactly its settings is in none of
// the lists: it is left alone, so its schedule is not moved by a restore that changes nothing.
type Plan struct {
	Added []string `json:"added"`
	// Updated are the shares whose settings differ from the file's.
	Updated []string `json:"updated"`
	// Removed shares lose their scan data with them: that is what replacing means, and the confirmation says so.
	Removed []string `json:"removed"`
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// current reads the shares as a backup would hold them, by name, with their ids.
func current(ctx context.Context, q querier) (ids map[string]int64, shares map[string]Portable, err error) {
	rows, err := q.Query(ctx, loadSQL)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids, shares = map[string]int64{}, map[string]Portable{}
	for rows.Next() {
		f, sec, err := scanFull(rows)
		if err != nil {
			return nil, nil, err
		}
		in := f.Input
		in.Password = nil
		if in.Kind != KindSMB {
			sec = nil
		}
		ids[in.Name], shares[in.Name] = f.ID, Portable{Input: in, Secret: sec}
	}
	return ids, shares, rows.Err()
}

func samePortable(a, b Portable) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func plan(cur map[string]Portable, items []Portable) Plan {
	p := Plan{Added: []string{}, Updated: []string{}, Removed: []string{}}
	keep := map[string]bool{}
	for _, it := range items {
		keep[it.Name] = true
		if have, ok := cur[it.Name]; !ok {
			p.Added = append(p.Added, it.Name)
		} else if !samePortable(have, it) {
			p.Updated = append(p.Updated, it.Name)
		}
	}
	for n := range cur {
		if !keep[n] {
			p.Removed = append(p.Removed, n)
		}
	}
	sort.Strings(p.Added)
	sort.Strings(p.Updated)
	sort.Strings(p.Removed)
	return p
}

// PlanFor says what restoring these shares would do now, without changing anything.
func (s *Service) PlanFor(ctx context.Context, items []Portable) (Plan, error) {
	_, cur, err := current(ctx, s.pool)
	if err != nil {
		return Plan{}, err
	}
	return plan(cur, items), nil
}

// ReplaceTx makes the shares of the database those of the file, inside the caller's transaction: a share with the same name keeps its row and
// its scan data and takes the new settings (only if they differ), a new name becomes a new, empty share, and a share the file does not have is
// removed with its scan data. The caller has taken the lock that keeps scans from starting and has checked that none is running. items must
// come from Prepare.
func (s *Service) ReplaceTx(ctx context.Context, tx pgx.Tx, items []Portable) (Plan, error) {
	ids, cur, err := current(ctx, tx)
	if err != nil {
		return Plan{}, err
	}
	p := plan(cur, items)
	todo := map[string]bool{}
	for _, n := range append(append([]string{}, p.Added...), p.Updated...) {
		todo[n] = true
	}
	for _, in := range items {
		if !todo[in.Name] {
			continue
		}
		next, err := s.nextRun(in.Cron)
		if err != nil {
			return Plan{}, err
		}
		var sec []byte
		if in.Kind == KindSMB {
			sec = in.Secret
		}
		if id, ok := ids[in.Name]; ok {
			_, err = tx.Exec(ctx, `
				UPDATE shares SET kind = $2, config = $3, excludes = $4, workers = $5, max_ops_per_sec = $6, max_ops_day = $7,
					day_start_hour = $8, day_end_hour = $9, cron = $10, next_run_at = $11, last_used_basis = $12, full_scan_every = $13,
					retention_scans = $14, retention_days = $15, snapshot_depth = $16, secret_enc = $17 WHERE id = $1`,
				id, in.Kind, configOf(in.Input), in.Excludes, in.Workers, in.MaxOpsPerSec, in.MaxOpsDay, in.DayStartHour, in.DayEndHour,
				in.Cron, next, in.LastUsedBasis, in.FullScanEvery, in.RetentionScans, in.RetentionDays, in.SnapshotDepth, sec)
		} else {
			var id int64
			err = tx.QueryRow(ctx, `
				INSERT INTO shares (name, kind, config, excludes, workers, max_ops_per_sec, max_ops_day, day_start_hour, day_end_hour, cron,
					next_run_at, last_used_basis, full_scan_every, retention_scans, retention_days, snapshot_depth, secret_enc)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
				in.Name, in.Kind, configOf(in.Input), in.Excludes, in.Workers, in.MaxOpsPerSec, in.MaxOpsDay, in.DayStartHour, in.DayEndHour,
				in.Cron, next, in.LastUsedBasis, in.FullScanEvery, in.RetentionScans, in.RetentionDays, in.SnapshotDepth, sec).Scan(&id)
			if err == nil {
				_, err = tx.Exec(ctx, `SELECT ensure_share_partitions($1)`, id)
			}
		}
		if err != nil {
			return Plan{}, fmt.Errorf("share %q: %w", in.Name, err)
		}
	}
	for _, name := range p.Removed {
		id := ids[name]
		if _, err := tx.Exec(ctx, `DELETE FROM shares WHERE id = $1`, id); err != nil {
			return Plan{}, fmt.Errorf("remove share %q: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `SELECT drop_share_partitions($1)`, id); err != nil {
			return Plan{}, fmt.Errorf("remove share %q: %w", name, err)
		}
	}
	return p, nil
}

// ---- file types ------------------------------------------------------------------------------

// FileType is one row of the file-type map as a backup holds it. The category is its key ("video"), not its number: a number says nothing to a
// person who opens the file.
type FileType struct {
	Ext      string `json:"ext"`
	Category string `json:"category"`
}

// Categories returns the category keys with their numbers.
func (s *Service) Categories(ctx context.Context) (map[string]int16, error) {
	return categoriesOf(ctx, s.pool)
}

func categoriesOf(ctx context.Context, q querier) (map[string]int16, error) {
	rows, err := q.Query(ctx, `SELECT key, id FROM categories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int16{}
	for rows.Next() {
		var k string
		var id int16
		if err := rows.Scan(&k, &id); err != nil {
			return nil, err
		}
		out[k] = id
	}
	return out, rows.Err()
}

// ExportFileTypes lists the whole extension map, by extension.
func (s *Service) ExportFileTypes(ctx context.Context) ([]FileType, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.ext, c.key FROM extension_map e JOIN categories c ON c.id = e.category_id ORDER BY e.ext`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileType{}
	for rows.Next() {
		var f FileType
		if err := rows.Scan(&f.Ext, &f.Category); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PrepareFileTypes holds the file types of a backup file to the rules of the file-types screen: a valid extension, a category that exists,
// and each extension once.
func PrepareFileTypes(items []FileType, cats map[string]int16) error {
	seen := make(map[string]int, len(items))
	for i, f := range items {
		field := func(n string) string { return fmt.Sprintf("file_types[%d].%s", i, n) }
		if !ValidExt(f.Ext) {
			return &ItemError{Field: field("ext"), Msg: fmt.Sprintf("%q is not an extension: use 1-16 lower-case letters or digits, without a dot", f.Ext)}
		}
		if _, ok := cats[f.Category]; !ok {
			return &ItemError{Field: field("category"), Msg: fmt.Sprintf("there is no category %q", f.Category)}
		}
		if j, dup := seen[f.Ext]; dup {
			return &ItemError{Field: field("ext"), Msg: fmt.Sprintf("the extension %q is also in file_types[%d]", f.Ext, j)}
		}
		seen[f.Ext] = i
	}
	return nil
}

// FileTypesChanged counts how many extensions a restore of these types would add, move or remove.
func (s *Service) FileTypesChanged(ctx context.Context, items []FileType) (int, error) {
	return fileTypesChanged(ctx, s.pool, items)
}

func fileTypesChanged(ctx context.Context, q querier, items []FileType) (int, error) {
	rows, err := q.Query(ctx, `SELECT e.ext, c.key FROM extension_map e JOIN categories c ON c.id = e.category_id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cur := map[string]string{}
	for rows.Next() {
		var e, k string
		if err := rows.Scan(&e, &k); err != nil {
			return 0, err
		}
		cur[e] = k
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	changed := 0
	keep := map[string]bool{}
	for _, f := range items {
		keep[f.Ext] = true
		if k, ok := cur[f.Ext]; !ok || k != f.Category {
			changed++
		}
	}
	for e := range cur {
		if !keep[e] {
			changed++
		}
	}
	return changed, nil
}

// ReplaceFileTypesTx makes the extension map that of the file, inside the caller's transaction, and returns how many extensions changed.
// items must have been through PrepareFileTypes.
func (s *Service) ReplaceFileTypesTx(ctx context.Context, tx pgx.Tx, items []FileType) (int, error) {
	cats, err := categoriesOf(ctx, tx)
	if err != nil {
		return 0, err
	}
	changed, err := fileTypesChanged(ctx, tx, items)
	if err != nil {
		return 0, err
	}
	exts := make([]string, len(items))
	ids := make([]int16, len(items))
	for i, f := range items {
		id, ok := cats[f.Category]
		if !ok {
			return 0, fmt.Errorf("there is no category %q", f.Category)
		}
		exts[i], ids[i] = f.Ext, id
	}
	if _, err := tx.Exec(ctx, `DELETE FROM extension_map WHERE ext <> ALL($1::text[])`, exts); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO extension_map (ext, category_id) SELECT * FROM unnest($1::text[], $2::smallint[])
		ON CONFLICT (ext) DO UPDATE SET category_id = EXCLUDED.category_id`, exts, ids); err != nil {
		return 0, err
	}
	return changed, nil
}

// SharesWithData lists the shares that have a finished scan, inside a transaction: the ones a changed file-type map has to be applied to.
func SharesWithData(ctx context.Context, tx pgx.Tx) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM shares WHERE last_scan_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
