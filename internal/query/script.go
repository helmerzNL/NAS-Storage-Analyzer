package query

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// ScriptFile is a selected file with what a delete script needs to know about it.
type ScriptFile struct {
	ID    int64
	Path  string // below the share root, with leading slash
	Size  int64
	Mtime time.Time
	Nlink int
	// Twin is true when another file of the same folder, or a folder next to one of its ancestors, has a name that is
	// the same when case is ignored. A client that ignores case (SMB from Windows, a Mac) cannot tell the two apart, so
	// a script must not name it.
	Twin bool
}

// ScriptSelection is the answer to FilesForScript.
type ScriptSelection struct {
	ScanID   int64     // the scan the index is from
	ScanTime time.Time // when it finished
	Files    []ScriptFile
	Missing  []int64 // ids that are not in the index of this share (gone since the page was loaded, or another share's)
}

// FilesForScript looks up the files a person ticked. Ids that are not files of this share come back in Missing
// without any name, so the call tells nothing about another share. The order of the ids is kept.
func (q *DB) FilesForScript(ctx context.Context, shareID int64, ids []int64) (*ScriptSelection, error) {
	for _, id := range ids {
		if id <= 0 {
			return nil, invalid("a file id must be a positive number")
		}
	}
	ids = uniqueIDs(ids)
	if _, err := q.share(ctx, shareID); err != nil {
		return nil, err
	}
	sel := &ScriptSelection{}
	var finished *time.Time
	var scan *int64
	if err := q.pool.QueryRow(ctx, `
		SELECT s.last_scan_id, sc.finished_at FROM shares s LEFT JOIN scans sc ON sc.id = s.last_scan_id WHERE s.id = $1`, shareID).
		Scan(&scan, &finished); err != nil {
		return nil, err
	}
	if scan == nil || finished == nil {
		return nil, ErrNoData
	}
	sel.ScanID, sel.ScanTime = *scan, *finished
	if len(ids) == 0 {
		return sel, nil
	}

	type row struct {
		dir  int64
		name string
		fold string // lower(name) as the database computes it, so the comparison below is the database's own
		f    ScriptFile
	}
	rows, err := q.pool.Query(ctx, `SELECT id, dir_id, name, lower(name), size, mtime, nlink FROM files WHERE share_id = $1 AND id = ANY($2)`, shareID, ids)
	if err != nil {
		return nil, err
	}
	found := make(map[int64]*row, len(ids))
	var dirIDs []int64
	for rows.Next() {
		r := &row{}
		if err := rows.Scan(&r.f.ID, &r.dir, &r.name, &r.fold, &r.f.Size, &r.f.Mtime, &r.f.Nlink); err != nil {
			rows.Close()
			return nil, err
		}
		found[r.f.ID] = r
		dirIDs = append(dirIDs, r.dir)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		sel.Missing = ids
		return sel, nil
	}
	dirIDs = uniqueIDs(dirIDs)

	paths, err := q.dirPaths(ctx, shareID, dirIDs)
	if err != nil {
		return nil, err
	}
	// Twins: a file that shares its folded name with another file of the same folder.
	type nameKey struct {
		dir  int64
		fold string
	}
	folds := make([]string, 0, len(found))
	seenFold := map[string]bool{}
	for _, r := range found {
		if !seenFold[r.fold] {
			seenFold[r.fold] = true
			folds = append(folds, r.fold)
		}
	}
	// Only the rows of these folders whose folded name is one of the selected ones come back: the folders are read
	// once, and what travels is the selection and its twins, not the folders.
	sib, err := q.pool.Query(ctx, `SELECT dir_id, lower(name) FROM files WHERE share_id = $1 AND dir_id = ANY($2) AND lower(name) = ANY($3)`,
		shareID, dirIDs, folds)
	if err != nil {
		return nil, err
	}
	count := map[nameKey]int{}
	for sib.Next() {
		var k nameKey
		if err := sib.Scan(&k.dir, &k.fold); err != nil {
			sib.Close()
			return nil, err
		}
		count[k]++
	}
	sib.Close()
	if err := sib.Err(); err != nil {
		return nil, err
	}
	dirTwins, chains, err := q.dirTwins(ctx, shareID, dirIDs)
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		r, ok := found[id]
		if !ok {
			sel.Missing = append(sel.Missing, id)
			continue
		}
		r.f.Path = joinPath(paths[r.dir], r.name)
		r.f.Twin = count[nameKey{r.dir, r.fold}] > 1
		for _, anc := range chains[r.dir] {
			if dirTwins[anc] {
				r.f.Twin = true
				break
			}
		}
		sel.Files = append(sel.Files, r.f)
	}
	return sel, nil
}

// dirTwins finds, for the directories that hold the selected files, which of their ancestors have a sibling folder
// whose name is equal when case is ignored. chains maps each of those directories to its ancestors below the share
// root (itself included).
//
// Only folders are compared with folders, and files with files. A file and a folder whose names differ only in case
// cannot be told apart by a client that ignores case either, but there the script fails safe: what it finds is not a
// regular file of the size it expects, and every script keeps such a thing.
//
// The names of the folders are grouped in the database, once per parent, and only the groups with more than one
// member come back. The comparison is lower() of the database: a database with a UTF-8 locale folds all of Unicode, one
// with the C locale only ASCII.
func (q *DB) dirTwins(ctx context.Context, share int64, dirIDs []int64) (twin map[int64]bool, chains map[int64][]int64, err error) {
	rows, err := q.pool.Query(ctx, `SELECT id, path::text FROM dirs WHERE share_id = $1 AND id = ANY($2)`, share, dirIDs)
	if err != nil {
		return nil, nil, err
	}
	chains = make(map[int64][]int64, len(dirIDs))
	var ancestors, parents []int64
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return nil, nil, err
		}
		labels := strings.Split(path, ".")
		chain := make([]int64, 0, len(labels))
		for i, l := range labels {
			if i == 0 {
				continue // the share root has no name and no siblings
			}
			n, _ := strconv.ParseInt(l, 10, 64)
			p, _ := strconv.ParseInt(labels[i-1], 10, 64)
			chain = append(chain, n)
			ancestors = append(ancestors, n)
			parents = append(parents, p)
		}
		chains[id] = chain
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	twin = map[int64]bool{}
	if len(ancestors) == 0 {
		return twin, chains, nil
	}
	tw, err := q.pool.Query(ctx, `
		WITH c AS (
			SELECT parent_id AS p, lower(name) AS k FROM dirs WHERE share_id = $1 AND parent_id = ANY($2)
			GROUP BY parent_id, lower(name) HAVING count(*) > 1)
		SELECT d.id FROM dirs d JOIN c ON c.p = d.parent_id AND c.k = lower(d.name)
		WHERE d.share_id = $1 AND d.id = ANY($3)`, share, uniqueIDs(parents), uniqueIDs(ancestors))
	if err != nil {
		return nil, nil, err
	}
	defer tw.Close()
	for tw.Next() {
		var id int64
		if err := tw.Scan(&id); err != nil {
			return nil, nil, err
		}
		twin[id] = true
	}
	return twin, chains, tw.Err()
}
