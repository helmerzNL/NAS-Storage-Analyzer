// Package query is the read side of the database: every number the API
// serves comes from here, and nothing here touches the file system.
package query

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB runs read queries. Hand it a pool whose sessions are read-only.
type DB struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

var (
	// ErrNotFound: the share, directory or scan does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNoData: the share exists but has no completed scan yet.
	ErrNoData = errors.New("share has no completed scan yet")
)

// InvalidError is returned for bad caller input (maps to HTTP 400).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &InvalidError{Msg: fmt.Sprintf(format, a...)} }

const (
	BasisAtime = "atime"
	BasisMtime = "mtime"
	BasisMax   = "max"

	defaultLimit = 100
	maxLimit     = 1000
)

func clampLimit(n int) (int, error) {
	switch {
	case n == 0:
		return defaultLimit, nil
	case n < 0:
		return 0, invalid("limit must be positive")
	case n > maxLimit:
		return 0, invalid("limit must be at most %d", maxLimit)
	}
	return n, nil
}

// args collects positional SQL parameters; $1 is always the share id.
type args struct{ v []any }

func newArgs(share int64) *args { return &args{v: []any{share}} }

func (a *args) add(x any) string {
	a.v = append(a.v, x)
	return "$" + strconv.Itoa(len(a.v))
}

// epochMicros turns a timestamp expression into a sortable bigint; NULL sorts
// as the epoch.
func epochMicros(expr string) string {
	return "(extract(epoch from COALESCE(" + expr + ", 'epoch'::timestamptz)) * 1000000)::bigint"
}

// fileUsed is the "last used" expression of a file for the share's basis.
func fileUsed(basis, alias string) string {
	switch basis {
	case BasisMtime:
		return alias + ".mtime"
	case BasisMax:
		return "GREATEST(" + alias + ".atime, " + alias + ".mtime)"
	}
	return alias + ".atime"
}

// dirUsed is the newest "last used" time among the files below a directory.
func dirUsed(basis, alias string) string {
	if alias != "" {
		alias += "."
	}
	switch basis {
	case BasisMtime:
		return alias + "newest_mtime"
	case BasisMax:
		return "GREATEST(" + alias + "newest_atime, " + alias + "newest_mtime)"
	}
	return alias + "newest_atime"
}

// cursor is the opaque keyset position handed to clients.
type cursor struct {
	Sort string `json:"s"`
	Desc bool   `json:"d"`
	Key  string `json:"k"`
	ID   int64  `json:"i"`
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s, sort string, desc bool) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return nil, invalid("invalid cursor")
	}
	if c.Sort != sort || c.Desc != desc {
		return nil, invalid("cursor does not belong to this sort order")
	}
	return &c, nil
}

// keysetCond builds "(key, id) </> ($k, $id)" for the given direction.
func keysetCond(a *args, keyExpr, idExpr string, c *cursor, numeric bool) (string, error) {
	var k any = c.Key
	if numeric {
		n, err := strconv.ParseInt(c.Key, 10, 64)
		if err != nil {
			return "", invalid("invalid cursor")
		}
		k = n
	}
	op := ">"
	if c.Desc {
		op = "<"
	}
	return fmt.Sprintf("(%s, %s) %s (%s, %s)", keyExpr, idExpr, op, a.add(k), a.add(c.ID)), nil
}

func dirWord(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

// Totals are size sums for a set of files.
type Totals struct {
	Size  int64 `json:"size"`
	Alloc int64 `json:"alloc"`
	Files int64 `json:"files"`
}

// shareMeta is what almost every query needs to know about a share.
type shareMeta struct {
	ID     int64
	Basis  string
	RootID int64
}

func (q *DB) share(ctx context.Context, id int64) (*shareMeta, error) {
	m := shareMeta{ID: id}
	var root *int64
	err := q.pool.QueryRow(ctx, `
		SELECT s.last_used_basis, (SELECT d.id FROM dirs d WHERE d.share_id = s.id AND d.parent_id IS NULL)
		FROM shares s WHERE s.id = $1`, id).Scan(&m.Basis, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, ErrNoData
	}
	m.RootID = *root
	return &m, nil
}

// dirRef is a directory's position in the tree.
type dirRef struct {
	ID    int64
	Depth int
	Path  string // ltree
}

func (q *DB) dirRef(ctx context.Context, share, dir int64) (*dirRef, error) {
	d := dirRef{ID: dir}
	err := q.pool.QueryRow(ctx, `SELECT depth, path::text FROM dirs WHERE share_id = $1 AND id = $2`, share, dir).Scan(&d.Depth, &d.Path)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

// dirPaths resolves directory ids to slash-separated paths below the share
// root ("" is the root, children look like "/movies/2024").
func (q *DB) dirPaths(ctx context.Context, share int64, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.pool.Query(ctx, `
		SELECT d.id, string_agg(a.name, '/' ORDER BY a.depth)
		FROM dirs d JOIN dirs a ON a.share_id = d.share_id AND a.path @> d.path
		WHERE d.share_id = $1 AND d.id = ANY($2) GROUP BY d.id`, share, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func joinPath(dir, name string) string { return dir + "/" + name }

// Filter narrows the files that count. The zero value matches everything.
type Filter struct {
	Categories     []int16
	Exts           []string // lower-case, without dot; "" matches files without extension
	MinSize        *int64
	MaxSize        *int64
	ModifiedBefore *time.Time
	ModifiedAfter  *time.Time
	UsedBefore     *time.Time
	UsedAfter      *time.Time
	Q              string // case-insensitive file-name substring (file lists only)
}

// Validate normalises the filter and rejects contradictory input.
func (f *Filter) Validate() error {
	for i, e := range f.Exts {
		f.Exts[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
	}
	if f.MinSize != nil && *f.MinSize < 0 || f.MaxSize != nil && *f.MaxSize < 0 {
		return invalid("size bounds must not be negative")
	}
	if f.MinSize != nil && f.MaxSize != nil && *f.MinSize > *f.MaxSize {
		return invalid("min_size is larger than max_size")
	}
	if f.ModifiedAfter != nil && f.ModifiedBefore != nil && !f.ModifiedAfter.Before(*f.ModifiedBefore) {
		return invalid("modified_after must be before modified_before")
	}
	if f.UsedAfter != nil && f.UsedBefore != nil && !f.UsedAfter.Before(*f.UsedBefore) {
		return invalid("used_after must be before used_before")
	}
	return nil
}

// Empty: no restriction on the files at all (the directory aggregates apply).
func (f Filter) Empty() bool {
	return len(f.Categories) == 0 && f.otherEmpty() && f.Q == ""
}

// categoriesOnly: only a category restriction, which dir_category_agg answers.
func (f Filter) categoriesOnly() bool {
	return len(f.Categories) > 0 && f.otherEmpty() && f.Q == ""
}

func (f Filter) otherEmpty() bool {
	return len(f.Exts) == 0 && f.MinSize == nil && f.MaxSize == nil && f.ModifiedBefore == nil &&
		f.ModifiedAfter == nil && f.UsedBefore == nil && f.UsedAfter == nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// conds returns SQL conditions on files aliased as alias.
func (f Filter) conds(a *args, basis, alias string) []string {
	var c []string
	col := func(n string) string { return alias + "." + n }
	if len(f.Categories) > 0 {
		c = append(c, col("category_id")+" = ANY("+a.add(f.Categories)+")")
	}
	if len(f.Exts) > 0 {
		c = append(c, col("ext")+" = ANY("+a.add(f.Exts)+")")
	}
	if f.MinSize != nil {
		c = append(c, col("size")+" >= "+a.add(*f.MinSize))
	}
	if f.MaxSize != nil {
		c = append(c, col("size")+" <= "+a.add(*f.MaxSize))
	}
	if f.ModifiedBefore != nil {
		c = append(c, col("mtime")+" < "+a.add(*f.ModifiedBefore))
	}
	if f.ModifiedAfter != nil {
		c = append(c, col("mtime")+" >= "+a.add(*f.ModifiedAfter))
	}
	if f.UsedBefore != nil {
		c = append(c, fileUsed(basis, alias)+" < "+a.add(*f.UsedBefore))
	}
	if f.UsedAfter != nil {
		c = append(c, fileUsed(basis, alias)+" >= "+a.add(*f.UsedAfter))
	}
	if f.Q != "" {
		c = append(c, col("name")+` ILIKE `+a.add("%"+likeEscaper.Replace(f.Q)+"%"))
	}
	return c
}

func where(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " AND " + strings.Join(conds, " AND ")
}
