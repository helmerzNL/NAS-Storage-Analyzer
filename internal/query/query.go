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
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB runs read queries. Hand it a pool whose sessions are read-only.
type DB struct {
	pool  *pgxpool.Pool
	paths *pathCache
}

func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool, paths: &pathCache{names: map[dirKey]string{}, paths: map[dirKey]string{}}}
}

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

// keysetCondKind is keysetCond for keys that may be text, integers or
// timestamps (carried in the cursor as unix microseconds).
func keysetCondKind(a *args, keyExpr, idExpr string, c *cursor, kind keyKind) (string, error) {
	if kind != keyTime {
		return keysetCond(a, keyExpr, idExpr, c, kind == keyInt)
	}
	n, err := strconv.ParseInt(c.Key, 10, 64)
	if err != nil {
		return "", invalid("invalid cursor")
	}
	op := ">"
	if c.Desc {
		op = "<"
	}
	return fmt.Sprintf("(%s, %s) %s (%s, %s)", keyExpr, idExpr, op, a.add(time.UnixMicro(n).UTC()), a.add(c.ID)), nil
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

type dirKey struct{ share, id int64 }

// pathCache remembers directory names and resolved paths. A directory id never
// changes its path (ids are not reused; a renamed directory gets a new id), so
// entries stay valid across scans. It is cleared when it grows too large.
type pathCache struct {
	mu    sync.Mutex
	names map[dirKey]string
	paths map[dirKey]string
}

const pathCacheMax = 500_000

func (c *pathCache) get(k dirKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.paths[k]
	return p, ok
}

// dirPaths resolves directory ids to slash-separated paths below the share
// root ("" is the root, children look like "/movies/2024"). Unknown ids are
// looked up with two primary-key queries: the ltree path of each directory
// lists its ancestor ids, whose names are then fetched in one batch.
func (q *DB) dirPaths(ctx context.Context, share int64, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	var miss []int64
	for _, id := range ids {
		if p, ok := q.paths.get(dirKey{share, id}); ok {
			out[id] = p
		} else {
			miss = append(miss, id)
		}
	}
	if len(miss) == 0 {
		return out, nil
	}
	rows, err := q.pool.Query(ctx, `SELECT id, path::text FROM dirs WHERE share_id = $1 AND id = ANY($2)`, share, miss)
	if err != nil {
		return nil, err
	}
	chains := make(map[int64][]int64, len(miss))
	need := map[int64]bool{}
	q.paths.mu.Lock()
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			q.paths.mu.Unlock()
			rows.Close()
			return nil, err
		}
		labels := strings.Split(path, ".")
		chain := make([]int64, 0, len(labels))
		for _, l := range labels {
			n, _ := strconv.ParseInt(l, 10, 64)
			chain = append(chain, n)
			if _, ok := q.paths.names[dirKey{share, n}]; !ok {
				need[n] = true
			}
		}
		chains[id] = chain
	}
	q.paths.mu.Unlock()
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(need) > 0 {
		ask := make([]int64, 0, len(need))
		for id := range need {
			ask = append(ask, id)
		}
		nr, err := q.pool.Query(ctx, `SELECT id, name FROM dirs WHERE share_id = $1 AND id = ANY($2)`, share, ask)
		if err != nil {
			return nil, err
		}
		q.paths.mu.Lock()
		for nr.Next() {
			var id int64
			var name string
			if err := nr.Scan(&id, &name); err != nil {
				q.paths.mu.Unlock()
				nr.Close()
				return nil, err
			}
			q.paths.names[dirKey{share, id}] = name
		}
		q.paths.mu.Unlock()
		nr.Close()
		if err := nr.Err(); err != nil {
			return nil, err
		}
	}
	q.paths.mu.Lock()
	defer q.paths.mu.Unlock()
	if len(q.paths.paths) > pathCacheMax || len(q.paths.names) > 2*pathCacheMax {
		q.paths.paths, q.paths.names = map[dirKey]string{}, map[dirKey]string{}
	}
	for id, chain := range chains {
		var b strings.Builder
		for _, anc := range chain[1:] { // the first label is the share root, whose name is empty
			b.WriteByte('/')
			b.WriteString(q.paths.names[dirKey{share, anc}])
		}
		out[id] = b.String()
		q.paths.paths[dirKey{share, id}] = b.String()
	}
	return out, nil
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
	// A single value is written as plain equality: only then can the planner
	// read (share, ext, size) or (share, category, size) in size order and stop
	// early. With "= ANY" it walks the size index and filters millions of rows.
	switch len(f.Categories) {
	case 0:
	case 1:
		c = append(c, col("category_id")+" = "+a.add(f.Categories[0]))
	default:
		c = append(c, col("category_id")+" = ANY("+a.add(f.Categories)+")")
	}
	switch len(f.Exts) {
	case 0:
	case 1:
		c = append(c, col("ext")+" = "+a.add(f.Exts[0]))
	default:
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
