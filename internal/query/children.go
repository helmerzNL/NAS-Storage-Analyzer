package query

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ChildQuery lists the subdirectories of a directory.
type ChildQuery struct {
	ShareID int64
	DirID   int64 // 0 = share root
	Filter  Filter
	Sort    string // size (default) | files | name | mtime | used
	Desc    bool
	Limit   int
	Cursor  string
	// ForceLive skips the pre-computed aggregates and always aggregates over
	// the files table (tier 3). For tests and benchmarks.
	ForceLive bool
}

// Child is one subdirectory row. Size, Alloc and Files count the files that
// match the filter (all files when there is no filter).
type Child struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Size  int64   `json:"size"`
	Alloc int64   `json:"alloc"`
	Files int64   `json:"files"`
	Dirs  int64   `json:"dirs"` // subdirectories below, regardless of the filter
	Pct   float64 `json:"pct"`  // share of the parent's matching size, 0..100
	// With only a category filter the four times describe all files of the
	// directory, not just the matching ones.
	OldestMtime *time.Time `json:"oldest_mtime"`
	NewestMtime *time.Time `json:"newest_mtime"`
	OldestAtime *time.Time `json:"oldest_atime"`
	NewestAtime *time.Time `json:"newest_atime"`

	usedMicros int64 // tier 3 only: newest last-used time of matching files
}

// ChildPage is a page of Child rows.
type ChildPage struct {
	Items      []Child `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
	// Parent holds the matching totals of the listed directory itself
	// (subtree), the denominator of Pct.
	Parent Totals `json:"parent"`
	// Tier says how the answer was produced: 1 pre-computed directory
	// aggregates, 2 per-category aggregates, 3 live aggregation over files.
	Tier int `json:"tier"`
}

var childSorts = map[string]bool{"size": true, "files": true, "name": true, "mtime": true, "used": true}

func normSort(s string, allowed map[string]bool) (string, error) {
	if s == "" {
		return "size", nil
	}
	if !allowed[s] {
		return "", invalid("unknown sort %q", s)
	}
	return s, nil
}

// childKeySQL is the sort key over the inner query's columns.
func childKeySQL(sort, basis string) (expr string, numeric bool) {
	switch sort {
	case "files":
		return "files", true
	case "name":
		return `name COLLATE "C"`, false
	case "mtime":
		return epochMicros("newest_mtime"), true
	case "used":
		return epochMicros(dirUsed(basis, "")), true
	}
	return "size", true
}

// Children lists subdirectories, choosing the cheapest correct source for the
// filter: pre-computed directory rows, per-category rows, or a live pass.
func (q *DB) Children(ctx context.Context, cq ChildQuery) (*ChildPage, error) {
	if err := cq.Filter.Validate(); err != nil {
		return nil, err
	}
	limit, err := clampLimit(cq.Limit)
	if err != nil {
		return nil, err
	}
	sortName, err := normSort(cq.Sort, childSorts)
	if err != nil {
		return nil, err
	}
	m, err := q.share(ctx, cq.ShareID)
	if err != nil {
		return nil, err
	}
	dirID := cq.DirID
	if dirID == 0 {
		dirID = m.RootID
	}
	d, err := q.dirRef(ctx, cq.ShareID, dirID)
	if err != nil {
		return nil, err
	}
	cur, err := decodeCursor(cq.Cursor, sortName, cq.Desc)
	if err != nil {
		return nil, err
	}

	f := cq.Filter
	switch {
	case cq.ForceLive:
	case f.Empty():
		return q.childrenSQL(ctx, m, d, f, 1, sortName, cq.Desc, limit, cur)
	case f.categoriesOnly() && (sortName == "size" || sortName == "files" || sortName == "name"):
		return q.childrenSQL(ctx, m, d, f, 2, sortName, cq.Desc, limit, cur)
	}
	return q.childrenLive(ctx, m, d, f, sortName, cq.Desc, limit, cur)
}

func (q *DB) childrenSQL(ctx context.Context, m *shareMeta, d *dirRef, f Filter, tier int, sortName string, desc bool, limit int, cur *cursor) (*ChildPage, error) {
	a := newArgs(m.ID)
	parent := a.add(d.ID)
	var inner string
	if tier == 1 {
		inner = `SELECT id, name, size_total AS size, alloc_total AS alloc, files_total AS files, dirs_total AS dirs,
				oldest_mtime, newest_mtime, oldest_atime, newest_atime
			FROM dirs WHERE share_id = $1 AND parent_id = ` + parent
	} else {
		inner = `SELECT c.id, c.name, sum(a.size_total)::bigint AS size, sum(a.alloc_total)::bigint AS alloc,
				sum(a.files_total)::bigint AS files, c.dirs_total AS dirs,
				c.oldest_mtime, c.newest_mtime, c.oldest_atime, c.newest_atime
			FROM dirs c JOIN dir_category_agg a ON a.share_id = c.share_id AND a.dir_id = c.id
				AND a.category_id = ANY(` + a.add(f.Categories) + `)
			WHERE c.share_id = $1 AND c.parent_id = ` + parent + ` GROUP BY c.share_id, c.id`
	}
	key, numeric := childKeySQL(sortName, m.Basis)
	sql := `SELECT id, name, size, alloc, files, dirs, oldest_mtime, newest_mtime, oldest_atime, newest_atime
		FROM (` + inner + `) r WHERE true`
	if cur != nil {
		cond, err := keysetCond(a, key, "id", cur, numeric)
		if err != nil {
			return nil, err
		}
		sql += " AND " + cond
	}
	sql += ` ORDER BY ` + key + ` ` + dirWord(desc) + `, id ` + dirWord(desc) + ` LIMIT ` + a.add(limit+1)

	rows, err := q.pool.Query(ctx, sql, a.v...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &ChildPage{Items: []Child{}, Tier: tier}
	for rows.Next() {
		var c Child
		if err := rows.Scan(&c.ID, &c.Name, &c.Size, &c.Alloc, &c.Files, &c.Dirs, &c.OldestMtime, &c.NewestMtime,
			&c.OldestAtime, &c.NewestAtime); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if page.Parent, err = q.matchingTotals(ctx, m, d, f); err != nil {
		return nil, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = nextCursor(sortName, desc, page.Items[limit-1], m.Basis)
	}
	for i := range page.Items {
		page.Items[i].Pct = pct(page.Items[i].Size, page.Parent.Size)
	}
	return page, nil
}

// childKeyValue is the Go-side counterpart of childKeySQL for cursors.
func childKeyValue(c Child, sortName, basis string) (num int64, str string) {
	switch sortName {
	case "files":
		return c.Files, ""
	case "name":
		return 0, c.Name
	case "mtime":
		return micros(c.NewestMtime), ""
	case "used":
		if c.usedMicros != 0 {
			return c.usedMicros, ""
		}
		switch basis {
		case BasisMtime:
			return micros(c.NewestMtime), ""
		case BasisMax:
			return max(micros(c.NewestAtime), micros(c.NewestMtime)), ""
		}
		return micros(c.NewestAtime), ""
	}
	return c.Size, ""
}

func micros(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMicro()
}

func nextCursor(sortName string, desc bool, last Child, basis string) string {
	num, str := childKeyValue(last, sortName, basis)
	k := str
	if sortName != "name" {
		k = strconv.FormatInt(num, 10)
	}
	return cursor{Sort: sortName, Desc: desc, Key: k, ID: last.ID}.encode()
}

// childrenLive aggregates the matching files of the whole subtree in one pass
// and attributes each file to the child directory it lives under, using the
// child label inside the ltree path. Directories without matching files are
// not listed.
func (q *DB) childrenLive(ctx context.Context, m *shareMeta, d *dirRef, f Filter, sortName string, desc bool, limit int, cur *cursor) (*ChildPage, error) {
	a := newArgs(m.ID)
	pathArg, nlevel := a.add(d.Path), a.add(d.Depth+1)
	conds := append([]string{"f.counted"}, f.conds(a, m.Basis, "f")...)
	rows, err := q.pool.Query(ctx, `
		SELECT CASE WHEN nlevel(x.path) = `+nlevel+`::int THEN NULL ELSE subpath(x.path, `+nlevel+`::int, 1)::text END AS lbl,
		       sum(f.size)::bigint, sum(f.alloc)::bigint, count(*),
		       min(f.mtime), max(f.mtime), min(f.atime), max(f.atime),
		       max(`+epochMicros(fileUsed(m.Basis, "f"))+`)
		FROM files f JOIN dirs x ON x.share_id = f.share_id AND x.id = f.dir_id
		WHERE f.share_id = $1 AND x.path <@ `+pathArg+`::ltree`+where(conds)+`
		GROUP BY 1`, a.v...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byLabel := map[string]*Child{}
	page := &ChildPage{Items: []Child{}, Tier: 3}
	for rows.Next() {
		var lbl *string
		var c Child
		if err := rows.Scan(&lbl, &c.Size, &c.Alloc, &c.Files, &c.OldestMtime, &c.NewestMtime, &c.OldestAtime,
			&c.NewestAtime, &c.usedMicros); err != nil {
			return nil, err
		}
		page.Parent.Size += c.Size
		page.Parent.Alloc += c.Alloc
		page.Parent.Files += c.Files
		if lbl != nil {
			byLabel[*lbl] = &c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	names, err := q.pool.Query(ctx, `SELECT id, name, dirs_total FROM dirs WHERE share_id = $1 AND parent_id = $2`, m.ID, d.ID)
	if err != nil {
		return nil, err
	}
	defer names.Close()
	var all []Child
	for names.Next() {
		var id, dirs int64
		var name string
		if err := names.Scan(&id, &name, &dirs); err != nil {
			return nil, err
		}
		if c, ok := byLabel[strconv.FormatInt(id, 10)]; ok {
			c.ID, c.Name, c.Dirs = id, name, dirs
			c.Pct = pct(c.Size, page.Parent.Size)
			all = append(all, *c)
		}
	}
	if err := names.Err(); err != nil {
		return nil, err
	}

	cmp := func(x, y Child) int {
		xn, xs := childKeyValue(x, sortName, m.Basis)
		yn, ys := childKeyValue(y, sortName, m.Basis)
		c := 0
		switch {
		case sortName == "name":
			c = strings.Compare(xs, ys)
		case xn < yn:
			c = -1
		case xn > yn:
			c = 1
		}
		if c == 0 {
			switch {
			case x.ID < y.ID:
				c = -1
			case x.ID > y.ID:
				c = 1
			}
		}
		if desc {
			return -c
		}
		return c
	}
	sort.Slice(all, func(i, j int) bool { return cmp(all[i], all[j]) < 0 })

	if cur != nil {
		var curKey Child
		curKey.ID = cur.ID
		switch sortName {
		case "name":
			curKey.Name = cur.Key
		default:
			n, err := strconv.ParseInt(cur.Key, 10, 64)
			if err != nil {
				return nil, invalid("invalid cursor")
			}
			switch sortName {
			case "files":
				curKey.Files = n
			case "mtime":
				t := time.UnixMicro(n)
				curKey.NewestMtime = &t
			case "used":
				curKey.usedMicros = n
			default:
				curKey.Size = n
			}
		}
		i := sort.Search(len(all), func(i int) bool { return cmp(all[i], curKey) > 0 })
		all = all[i:]
	}
	if len(all) > limit {
		page.Items = all[:limit]
		page.NextCursor = nextCursor(sortName, desc, page.Items[limit-1], m.Basis)
	} else {
		page.Items = all
	}
	return page, nil
}

// matchingTotals returns the size of the files below directory d that match
// the filter, by the cheapest correct source.
func (q *DB) matchingTotals(ctx context.Context, m *shareMeta, d *dirRef, f Filter) (Totals, error) {
	var t Totals
	switch {
	case f.Empty():
		err := q.pool.QueryRow(ctx, `SELECT size_total, alloc_total, files_total FROM dirs WHERE share_id = $1 AND id = $2`,
			m.ID, d.ID).Scan(&t.Size, &t.Alloc, &t.Files)
		return t, err
	case f.categoriesOnly():
		err := q.pool.QueryRow(ctx, `SELECT coalesce(sum(size_total), 0)::bigint, coalesce(sum(alloc_total), 0)::bigint,
			coalesce(sum(files_total), 0)::bigint FROM dir_category_agg
			WHERE share_id = $1 AND dir_id = $2 AND category_id = ANY($3)`, m.ID, d.ID, f.Categories).Scan(&t.Size, &t.Alloc, &t.Files)
		return t, err
	}
	a := newArgs(m.ID)
	conds := append([]string{"f.counted"}, f.conds(a, m.Basis, "f")...)
	join, scope := "", ""
	if d.ID != m.RootID {
		join = " JOIN dirs x ON x.share_id = f.share_id AND x.id = f.dir_id"
		scope = " AND x.path <@ " + a.add(d.Path) + "::ltree"
	}
	err := q.pool.QueryRow(ctx, `SELECT coalesce(sum(f.size), 0)::bigint, coalesce(sum(f.alloc), 0)::bigint, count(*)
		FROM files f`+join+` WHERE f.share_id = $1`+scope+where(conds), a.v...).Scan(&t.Size, &t.Alloc, &t.Files)
	return t, err
}
