package query

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// FileQuery lists files, optionally below a directory.
type FileQuery struct {
	ShareID int64
	DirID   int64 // 0 = whole share
	// Recursive includes files in subdirectories. With DirID 0 the whole
	// share is always searched.
	Recursive bool
	Filter    Filter
	Sort      string // size (default) | name | mtime | used | ext
	Desc      bool
	Limit     int
	Cursor    string
	// CountedOnly hides the second and later hardlinks of an inode, so big-file
	// lists do not show the same data twice.
	CountedOnly bool
}

// File is one file row.
type File struct {
	ID       int64     `json:"id"`
	DirID    int64     `json:"dir_id"`
	Name     string    `json:"name"`
	Path     string    `json:"path"` // below the share root, with leading slash
	Ext      string    `json:"ext"`
	Category int16     `json:"category_id"`
	Size     int64     `json:"size"`
	Alloc    int64     `json:"alloc"`
	Mtime    time.Time `json:"mtime"`
	Atime    time.Time `json:"atime"`
	Ctime    time.Time `json:"ctime"`
	Nlink    int       `json:"nlink"`
	// Counted is false for extra hardlinks of an inode, whose size is not
	// added to any total.
	Counted bool `json:"counted"`
}

// FilePage is a page of files.
type FilePage struct {
	Items      []File `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

var fileSorts = map[string]bool{"size": true, "name": true, "mtime": true, "used": true, "ext": true}

// keyKind says how a sort key travels in a cursor.
type keyKind int

const (
	keyInt  keyKind = iota // bigint
	keyText                // text, byte order
	keyTime                // timestamptz, carried as unix microseconds
)

// fileKeySQL returns the ORDER BY expression. Date keys are the bare column so
// the (share_id, mtime) / (share_id, atime) indexes can serve the ordering.
func fileKeySQL(sortName, basis string) (expr string, kind keyKind) {
	switch sortName {
	case "name":
		return `f.name COLLATE "C"`, keyText
	case "ext":
		return `f.ext COLLATE "C"`, keyText
	case "mtime":
		return "f.mtime", keyTime
	case "used":
		return fileUsed(basis, "f"), keyTime
	}
	return "f.size", keyInt
}

// Files lists files matching the filter in the requested order.
func (q *DB) Files(ctx context.Context, fq FileQuery) (*FilePage, error) {
	if err := fq.Filter.Validate(); err != nil {
		return nil, err
	}
	limit, err := clampLimit(fq.Limit)
	if err != nil {
		return nil, err
	}
	sortName, err := normSort(fq.Sort, fileSorts)
	if err != nil {
		return nil, err
	}
	m, err := q.share(ctx, fq.ShareID)
	if err != nil {
		return nil, err
	}
	cur, err := decodeCursor(fq.Cursor, sortName, fq.Desc)
	if err != nil {
		return nil, err
	}

	a := newArgs(m.ID)
	conds := fq.Filter.conds(a, m.Basis, "f")
	if fq.CountedOnly {
		conds = append(conds, "f.counted")
	}
	switch {
	case fq.DirID == 0 && fq.Recursive, fq.DirID == m.RootID && fq.Recursive:
		// whole share: no scope condition
	case fq.DirID == 0:
		conds = append(conds, "f.dir_id = "+a.add(m.RootID))
	case fq.Recursive:
		ref, err := q.dirRef(ctx, m.ID, fq.DirID)
		if err != nil {
			return nil, err
		}
		conds = append(conds, "f.dir_id IN (SELECT x.id FROM dirs x WHERE x.share_id = $1 AND x.path <@ "+a.add(ref.Path)+"::ltree)")
	default:
		if _, err := q.dirRef(ctx, m.ID, fq.DirID); err != nil {
			return nil, err
		}
		conds = append(conds, "f.dir_id = "+a.add(fq.DirID))
	}
	key, kind := fileKeySQL(sortName, m.Basis)
	cursorKey := "(" + key + ")::text"
	if kind == keyTime {
		cursorKey = "(extract(epoch from " + key + ") * 1000000)::bigint::text"
	}
	if cur != nil {
		cond, err := keysetCondKind(a, key, "f.id", cur, kind)
		if err != nil {
			return nil, err
		}
		conds = append(conds, cond)
	}
	rows, err := q.pool.Query(ctx, `
		SELECT f.id, f.dir_id, f.name, f.ext, f.category_id, f.size, f.alloc, f.mtime, f.atime, f.ctime, f.nlink, f.counted,
		       `+cursorKey+`
		FROM files f WHERE f.share_id = $1`+where(conds)+`
		ORDER BY `+key+` `+dirWord(fq.Desc)+`, f.id `+dirWord(fq.Desc)+` LIMIT `+a.add(limit+1), a.v...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &FilePage{Items: []File{}}
	var keys []string
	for rows.Next() {
		var f File
		var k string
		if err := rows.Scan(&f.ID, &f.DirID, &f.Name, &f.Ext, &f.Category, &f.Size, &f.Alloc, &f.Mtime, &f.Atime,
			&f.Ctime, &f.Nlink, &f.Counted, &k); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, f)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = cursor{Sort: sortName, Desc: fq.Desc, Key: keys[limit-1], ID: page.Items[limit-1].ID}.encode()
	}
	if err := q.fillFilePaths(ctx, m.ID, page.Items); err != nil {
		return nil, err
	}
	return page, nil
}

func (q *DB) fillFilePaths(ctx context.Context, share int64, files []File) error {
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.DirID
	}
	paths, err := q.dirPaths(ctx, share, uniqueIDs(ids))
	if err != nil {
		return err
	}
	for i := range files {
		files[i].Path = joinPath(paths[files[i].DirID], files[i].Name)
	}
	return nil
}

// SearchQuery looks for files and directories by name.
type SearchQuery struct {
	ShareID int64 // 0 = all shares
	Q       string
	Kind    string // all (default) | files | dirs
	Limit   int
}

// SearchHit is one name match.
type SearchHit struct {
	ShareID int64      `json:"share_id"`
	Kind    string     `json:"kind"` // file | dir
	ID      int64      `json:"id"`
	DirID   int64      `json:"dir_id"` // containing directory (for a dir: its parent)
	Name    string     `json:"name"`
	Path    string     `json:"path"`
	Size    int64      `json:"size"`
	Mtime   *time.Time `json:"mtime"`
}

// SearchResult is the answer to a name search.
type SearchResult struct {
	Items []SearchHit `json:"items"`
	// Truncated: more than searchCandidates files matched, so the list shows the
	// largest of the first searchCandidates matches found, not necessarily the
	// largest overall. The caller should ask the user to refine the search.
	Truncated bool `json:"truncated"`
}

// MinSearchLen is the shortest search string; shorter patterns cannot use the
// trigram index and would scan every row.
const MinSearchLen = 3

// Search finds names containing Q (case-insensitive), largest first.
func (q *DB) Search(ctx context.Context, sq SearchQuery) (*SearchResult, error) {
	term := strings.TrimSpace(sq.Q)
	if len([]rune(term)) < MinSearchLen {
		return nil, invalid("search text must be at least %d characters", MinSearchLen)
	}
	limit, err := clampLimit(sq.Limit)
	if err != nil {
		return nil, err
	}
	if limit > 200 {
		return nil, invalid("limit must be at most 200 for search")
	}
	switch sq.Kind {
	case "", "all", "files", "dirs":
	default:
		return nil, invalid("kind must be all, files or dirs")
	}
	if sq.ShareID != 0 {
		if _, err := q.share(ctx, sq.ShareID); err != nil {
			return nil, err
		}
	}
	pattern := "%" + likeEscaper.Replace(term) + "%"
	hits := []SearchHit{}

	truncated := false
	if sq.Kind != "dirs" {
		found, trunc, err := q.searchFiles(ctx, sq.ShareID, term, pattern, limit)
		if err != nil {
			return nil, err
		}
		hits, truncated = append(hits, found...), trunc
	}
	if sq.Kind != "files" {
		rows, err := q.pool.Query(ctx, `
			SELECT d.share_id, d.id, d.parent_id, d.name, d.size_total, d.newest_mtime FROM dirs d
			WHERE ($1 = 0 OR d.share_id = $1) AND d.parent_id IS NOT NULL AND d.name ILIKE $2
			ORDER BY d.size_total DESC, d.id LIMIT $3`, sq.ShareID, pattern, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			h := SearchHit{Kind: "dir"}
			if err := rows.Scan(&h.ShareID, &h.ID, &h.DirID, &h.Name, &h.Size, &h.Mtime); err != nil {
				rows.Close()
				return nil, err
			}
			hits = append(hits, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Size > hits[j].Size })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	// Resolve paths per share: files live in DirID, directories are their own
	// path.
	byShare := map[int64][]int64{}
	for _, h := range hits {
		id := h.DirID
		if h.Kind == "dir" {
			id = h.ID
		}
		byShare[h.ShareID] = append(byShare[h.ShareID], id)
	}
	paths := map[int64]map[int64]string{}
	for share, ids := range byShare {
		if paths[share], err = q.dirPaths(ctx, share, uniqueIDs(ids)); err != nil {
			return nil, err
		}
	}
	for i := range hits {
		h := &hits[i]
		if h.Kind == "dir" {
			h.Path = paths[h.ShareID][h.ID]
		} else {
			h.Path = joinPath(paths[h.ShareID][h.DirID], h.Name)
		}
	}
	return &SearchResult{Items: hits, Truncated: truncated}, nil
}

// searchCandidates bounds how many name matches are examined for one search.
var searchCandidates = 20_000

// SetSearchCandidates lowers the bound; tests use it to reach the truncated
// path with small data. It returns a function that restores the old value.
func SetSearchCandidates(n int) (restore func()) {
	old := searchCandidates
	searchCandidates = n
	return func() { searchCandidates = old }
}

var extLike = regexp.MustCompile(`^\.[A-Za-z0-9]{1,16}$`)

// searchFiles returns the largest files whose name matches.
//
// A term that looks like an extension (".jpg") is answered exactly through the
// (share, ext, size) index. Otherwise the trigram index yields the candidates;
// when they exceed searchCandidates, the largest of the first searchCandidates
// found are returned and truncated is set. Ordering millions of matches by size
// would take seconds, and a result that tells the user to be more specific
// serves them better than a slow exact answer to a vague question.
func (q *DB) searchFiles(ctx context.Context, share int64, term, pattern string, limit int) (hits []SearchHit, truncated bool, err error) {
	scan := func(rows pgx.Rows) ([]SearchHit, error) {
		defer rows.Close()
		var out []SearchHit
		for rows.Next() {
			h := SearchHit{Kind: "file"}
			var mt time.Time
			if err := rows.Scan(&h.ShareID, &h.ID, &h.DirID, &h.Name, &h.Size, &mt); err != nil {
				return nil, err
			}
			h.Mtime = &mt
			out = append(out, h)
		}
		return out, rows.Err()
	}
	if extLike.MatchString(term) {
		rows, err := q.pool.Query(ctx, `
			SELECT f.share_id, f.id, f.dir_id, f.name, f.size, f.mtime FROM files f
			WHERE ($1 = 0 OR f.share_id = $1) AND f.counted AND f.ext = $2
			ORDER BY f.size DESC, f.id LIMIT $3`, share, strings.ToLower(term[1:]), limit)
		if err != nil {
			return nil, false, err
		}
		hits, err = scan(rows)
		return hits, false, err
	}
	rows, err := q.pool.Query(ctx, `
		SELECT f.share_id, f.id, f.dir_id, f.name, f.size, f.mtime FROM files f
		WHERE ($1 = 0 OR f.share_id = $1) AND f.counted AND f.name ILIKE $2 LIMIT $3`, share, pattern, searchCandidates+1)
	if err != nil {
		return nil, false, err
	}
	cand, err := scan(rows)
	if err != nil {
		return nil, false, err
	}
	if truncated = len(cand) > searchCandidates; truncated {
		cand = cand[:searchCandidates]
	}
	sort.SliceStable(cand, func(i, j int) bool { return cand[i].Size > cand[j].Size })
	if len(cand) > limit {
		cand = cand[:limit]
	}
	return cand, truncated, nil
}
