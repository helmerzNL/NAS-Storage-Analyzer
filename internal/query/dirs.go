package query

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Crumb is one step of the path from the share root to a directory.
type Crumb struct {
	ID   int64  `json:"id"`
	Name string `json:"name"` // "" for the share root
}

// DirDetail describes one directory.
type DirDetail struct {
	ID         int64   `json:"id"`
	ParentID   *int64  `json:"parent_id"`
	Name       string  `json:"name"`
	Depth      int     `json:"depth"`
	Path       string  `json:"path"`
	Breadcrumb []Crumb `json:"breadcrumb"`
	// Total covers the whole subtree, Direct only files directly inside.
	Total       Totals     `json:"total"`
	Direct      Totals     `json:"direct"`
	Dirs        int64      `json:"dirs"`        // subdirectories in the whole subtree
	DirsDirect  int64      `json:"dirs_direct"` // immediate subdirectories
	OldestMtime *time.Time `json:"oldest_mtime"`
	NewestMtime *time.Time `json:"newest_mtime"`
	OldestAtime *time.Time `json:"oldest_atime"`
	NewestAtime *time.Time `json:"newest_atime"`
	// Matching is set when a filter was given: the subtree totals of the
	// files that match it.
	Matching   *Totals         `json:"matching,omitempty"`
	Categories []CategoryTotal `json:"categories"`
}

// Dir returns details of a directory (0 = share root).
func (q *DB) Dir(ctx context.Context, share, dir int64, f Filter) (*DirDetail, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	m, err := q.share(ctx, share)
	if err != nil {
		return nil, err
	}
	if dir == 0 {
		dir = m.RootID
	}
	var d DirDetail
	var pathL string
	err = q.pool.QueryRow(ctx, `
		SELECT id, parent_id, name, depth, path::text, size_total, alloc_total, files_total, dirs_total,
		       size_direct, alloc_direct, files_direct, dirs_direct,
		       oldest_mtime, newest_mtime, oldest_atime, newest_atime
		FROM dirs WHERE share_id = $1 AND id = $2`, share, dir).Scan(&d.ID, &d.ParentID, &d.Name, &d.Depth, &pathL,
		&d.Total.Size, &d.Total.Alloc, &d.Total.Files, &d.Dirs, &d.Direct.Size, &d.Direct.Alloc, &d.Direct.Files,
		&d.DirsDirect, &d.OldestMtime, &d.NewestMtime, &d.OldestAtime, &d.NewestAtime)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := q.pool.Query(ctx, `SELECT id, name FROM dirs WHERE share_id = $1 AND path @> $2::ltree ORDER BY depth`, share, pathL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Breadcrumb = []Crumb{}
	for rows.Next() {
		var c Crumb
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		d.Breadcrumb = append(d.Breadcrumb, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// Path of the root is "", of /a/b the join of the names below the root.
	d.Path = ""
	for i, c := range d.Breadcrumb {
		if i > 0 {
			d.Path += "/" + c.Name
		}
	}

	if !f.Empty() {
		t, err := q.matchingTotals(ctx, m, &dirRef{ID: d.ID, Depth: d.Depth, Path: pathL}, f)
		if err != nil {
			return nil, err
		}
		d.Matching = &t
	}
	if d.Categories, err = q.Categories(ctx, share, d.ID); err != nil {
		return nil, err
	}
	return &d, nil
}

// DirRow is a directory in a ranked list.
type DirRow struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        int64      `json:"size"`
	Alloc       int64      `json:"alloc"`
	Files       int64      `json:"files"`
	Dirs        int64      `json:"dirs"`
	SizeDirect  int64      `json:"size_direct"`
	FilesDirect int64      `json:"files_direct"`
	OldestMtime *time.Time `json:"oldest_mtime"`
	NewestMtime *time.Time `json:"newest_mtime"`
	OldestAtime *time.Time `json:"oldest_atime"`
	NewestAtime *time.Time `json:"newest_atime"`
}

// DirListQuery ranks directories (largest, or cold).
type DirListQuery struct {
	ShareID    int64
	UnderDirID int64 // 0 = whole share
	// By chooses what "size" means: "total" (whole subtree, the default) or
	// "direct" (only files directly inside, which avoids listing every
	// ancestor of a big directory).
	By         string
	Sort       string // size (default) | used
	Desc       bool
	MinDepth   int // default 1: skip the share root
	UsedBefore *time.Time
	// Maximal: for cold lists, skip a directory whose parent is cold as well,
	// so a cold folder is not listed once per ancestor.
	Maximal bool
	Limit   int
	Cursor  string
}

// DirPage is a page of ranked directories.
type DirPage struct {
	Items      []DirRow `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// Dirs returns a ranked list of directories.
func (q *DB) Dirs(ctx context.Context, dq DirListQuery) (*DirPage, error) {
	limit, err := clampLimit(dq.Limit)
	if err != nil {
		return nil, err
	}
	sortName, err := normSort(dq.Sort, map[string]bool{"size": true, "used": true})
	if err != nil {
		return nil, err
	}
	by := dq.By
	switch by {
	case "":
		by = "total"
	case "total", "direct":
	default:
		return nil, invalid("by must be total or direct")
	}
	if dq.MinDepth < 0 {
		return nil, invalid("min_depth must not be negative")
	}
	m, err := q.share(ctx, dq.ShareID)
	if err != nil {
		return nil, err
	}
	cur, err := decodeCursor(dq.Cursor, sortName+"/"+by, dq.Desc)
	if err != nil {
		return nil, err
	}

	a := newArgs(m.ID)
	minDepth := dq.MinDepth
	if minDepth == 0 && dq.UnderDirID == 0 {
		minDepth = 1
	}
	conds := []string{"d.depth >= " + a.add(minDepth)}
	sizeCol := "d.size_total"
	if by == "direct" {
		sizeCol = "d.size_direct"
	}
	conds = append(conds, sizeCol+" > 0")
	if dq.UnderDirID != 0 && dq.UnderDirID != m.RootID {
		ref, err := q.dirRef(ctx, m.ID, dq.UnderDirID)
		if err != nil {
			return nil, err
		}
		conds = append(conds, "d.path <@ "+a.add(ref.Path)+"::ltree", "d.id <> "+a.add(ref.ID))
	} else if dq.UnderDirID != 0 {
		conds = append(conds, "d.id <> "+a.add(m.RootID))
	}
	if dq.UsedBefore != nil {
		t := a.add(*dq.UsedBefore)
		conds = append(conds, dirUsed(m.Basis, "d")+" < "+t)
		if dq.Maximal {
			conds = append(conds, `NOT EXISTS (SELECT 1 FROM dirs p WHERE p.share_id = d.share_id AND p.id = d.parent_id AND `+
				dirUsed(m.Basis, "p")+" < "+t+")")
		}
	}
	key := sizeCol
	if sortName == "used" {
		key = epochMicros(dirUsed(m.Basis, "d"))
	}
	if cur != nil {
		cond, err := keysetCond(a, key, "d.id", cur, true)
		if err != nil {
			return nil, err
		}
		conds = append(conds, cond)
	}
	rows, err := q.pool.Query(ctx, `
		SELECT d.id, d.name, d.size_total, d.alloc_total, d.files_total, d.dirs_total, d.size_direct, d.files_direct,
		       d.oldest_mtime, d.newest_mtime, d.oldest_atime, d.newest_atime, `+key+`
		FROM dirs d WHERE d.share_id = $1`+where(conds)+`
		ORDER BY `+key+` `+dirWord(dq.Desc)+`, d.id `+dirWord(dq.Desc)+` LIMIT `+a.add(limit+1), a.v...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &DirPage{Items: []DirRow{}}
	var keys []int64
	for rows.Next() {
		var r DirRow
		var k int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Size, &r.Alloc, &r.Files, &r.Dirs, &r.SizeDirect, &r.FilesDirect,
			&r.OldestMtime, &r.NewestMtime, &r.OldestAtime, &r.NewestAtime, &k); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, r)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = cursor{Sort: sortName + "/" + by, Desc: dq.Desc, Key: itoa(keys[limit-1]), ID: page.Items[limit-1].ID}.encode()
	}
	ids := make([]int64, len(page.Items))
	for i, r := range page.Items {
		ids[i] = r.ID
	}
	paths, err := q.dirPaths(ctx, m.ID, ids)
	if err != nil {
		return nil, err
	}
	for i := range page.Items {
		page.Items[i].Path = paths[page.Items[i].ID]
	}
	return page, nil
}

// TreeNode is a node of the treemap tree.
type TreeNode struct {
	ID    int64  `json:"id,omitempty"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Files int64  `json:"files"`
	// Kind is "dir", "files" (files directly inside the parent) or "other"
	// (the remaining small subdirectories).
	Kind     string     `json:"kind"`
	Children []TreeNode `json:"children,omitempty"`
}

type treeBuild struct {
	TreeNode
	direct   int64
	children []*treeBuild
}

func (t *treeBuild) out() TreeNode {
	n := t.TreeNode
	for _, c := range t.children {
		n.Children = append(n.Children, c.out())
	}
	return n
}

// Treemap returns the biggest subdirectories of a directory down to depth
// levels (1-3) as a tree. Each directory shows at most maxChildren
// subdirectories; the rest is folded into an "other" node, and files directly
// inside a directory become a "files" node, so child sizes add up to the
// parent. categories optionally restricts the sizes to those categories.
func (q *DB) Treemap(ctx context.Context, share, dir int64, categories []int16, depth, maxChildren int) (*TreeNode, error) {
	if depth == 0 {
		depth = 2
	}
	if depth < 1 || depth > 3 {
		return nil, invalid("depth must be between 1 and 3")
	}
	if maxChildren == 0 {
		maxChildren = 20
	}
	if maxChildren < 1 || maxChildren > 100 {
		return nil, invalid("max_children must be between 1 and 100")
	}
	m, err := q.share(ctx, share)
	if err != nil {
		return nil, err
	}
	if dir == 0 {
		dir = m.RootID
	}
	f := Filter{Categories: categories}
	d, err := q.dirRef(ctx, share, dir)
	if err != nil {
		return nil, err
	}
	var name string
	if err := q.pool.QueryRow(ctx, `SELECT name FROM dirs WHERE share_id = $1 AND id = $2`, share, dir).Scan(&name); err != nil {
		return nil, err
	}
	total, err := q.matchingTotals(ctx, m, d, f)
	if err != nil {
		return nil, err
	}
	root := &treeBuild{TreeNode: TreeNode{ID: dir, Name: name, Size: total.Size, Files: total.Files, Kind: "dir"}}
	if f.Empty() {
		err = q.pool.QueryRow(ctx, `SELECT size_direct FROM dirs WHERE share_id = $1 AND id = $2`, share, dir).Scan(&root.direct)
	} else {
		err = q.pool.QueryRow(ctx, `SELECT coalesce(sum(size_direct), 0)::bigint FROM dir_category_agg
			WHERE share_id = $1 AND dir_id = $2 AND category_id = ANY($3)`, share, dir, categories).Scan(&root.direct)
	}
	if err != nil {
		return nil, err
	}

	level := []*treeBuild{root}
	for lv := 0; lv < depth && len(level) > 0; lv++ {
		byID := make(map[int64]*treeBuild, len(level))
		ids := make([]int64, len(level))
		for i, n := range level {
			byID[n.ID], ids[i] = n, n.ID
		}
		a := newArgs(m.ID)
		var src string
		if f.Empty() {
			src = `SELECT id, parent_id, name, size_total AS size, files_total AS files, size_direct AS direct
				FROM dirs WHERE share_id = $1 AND parent_id = ANY(` + a.add(ids) + `) AND size_total > 0`
		} else {
			src = `SELECT c.id, c.parent_id, c.name, sum(a.size_total)::bigint AS size, sum(a.files_total)::bigint AS files,
					sum(a.size_direct)::bigint AS direct
				FROM dirs c JOIN dir_category_agg a ON a.share_id = c.share_id AND a.dir_id = c.id
					AND a.category_id = ANY(` + a.add(categories) + `)
				WHERE c.share_id = $1 AND c.parent_id = ANY(` + a.add(ids) + `) GROUP BY c.share_id, c.id`
		}
		rows, err := q.pool.Query(ctx, `SELECT id, parent_id, name, size, files, direct FROM (
				SELECT *, row_number() OVER (PARTITION BY parent_id ORDER BY size DESC, id) AS rn FROM (`+src+`) s) r
			WHERE rn <= `+a.add(maxChildren)+` ORDER BY parent_id, rn`, a.v...)
		if err != nil {
			return nil, err
		}
		var next []*treeBuild
		shown := map[int64]int64{}
		for rows.Next() {
			n := &treeBuild{TreeNode: TreeNode{Kind: "dir"}}
			var parent int64
			if err := rows.Scan(&n.ID, &parent, &n.Name, &n.Size, &n.Files, &n.direct); err != nil {
				rows.Close()
				return nil, err
			}
			p := byID[parent]
			p.children = append(p.children, n)
			shown[parent] += n.Size
			next = append(next, n)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rows.Close()
		for _, p := range level {
			if p.direct > 0 {
				p.children = append(p.children, &treeBuild{TreeNode: TreeNode{Name: "(files)", Size: p.direct, Kind: "files"}})
			}
			if rest := p.Size - shown[p.ID] - p.direct; rest > 0 {
				p.children = append(p.children, &treeBuild{TreeNode: TreeNode{Name: "(other)", Size: rest, Kind: "other"}})
			}
		}
		level = next
	}
	out := root.out()
	return &out, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
