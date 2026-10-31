package api

import (
	"net/http"
	"strconv"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
)

// streamPaged exports every page of a paged query as CSV. The first page is
// fetched before any output so that errors still become proper problem
// responses.
func streamPaged[T any](s *Server, w http.ResponseWriter, filename string, header []string,
	fetch func(cursor string) ([]T, string, error), row func(T) []string) error {
	rows, next, err := fetch("")
	if err != nil {
		return err
	}
	cs := newCSVStream(w, filename, header, s.cfg.CSVMaxRows)
	for {
		for _, it := range rows {
			if cs.rows >= s.cfg.CSVMaxRows {
				return cs.done()
			}
			if err := cs.row(row(it)...); err != nil {
				return err
			}
		}
		if next == "" || cs.rows >= s.cfg.CSVMaxRows {
			return cs.done()
		}
		if rows, next, err = fetch(next); err != nil {
			return err
		}
	}
}

// streamList exports an unpaged list as CSV.
func streamList[T any](s *Server, w http.ResponseWriter, filename string, header []string, list []T, row func(T) []string) error {
	cs := newCSVStream(w, filename, header, s.cfg.CSVMaxRows)
	for _, it := range list {
		if err := cs.row(row(it)...); err != nil {
			return err
		}
	}
	return cs.done()
}

func shareID(r *http.Request) (int64, error) { return parseID(r.PathValue("share"), "share") }

func parseBool(r *http.Request, key string, def bool) (bool, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, bad("%s must be true or false", key)
	}
	return b, nil
}

func parseIntParam(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, bad("%s must be an integer", key)
	}
	return n, nil
}

// ---- shares and scans -------------------------------------------------------

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) error {
	shares, err := s.q.Shares(r.Context())
	if err != nil {
		return err
	}
	if csv, err := wantsCSV(r); err != nil {
		return err
	} else if csv {
		return streamList(s, w, "shares.csv",
			[]string{"id", "name", "kind", "last_used_basis", "files", "dirs", "bytes", "alloc_bytes", "fs_total_bytes", "fs_free_bytes", "last_scan_finished"},
			shares, func(x query.ShareInfo) []string {
				row := []string{i64(x.ID), textCell(x.Name), x.Kind, x.LastUsedBasis, "", "", "", "", "", "", ""}
				if sc := x.LastScan; sc != nil {
					row[4], row[5], row[6], row[7], row[10] = i64(sc.Files), i64(sc.Dirs), i64(sc.Bytes), i64(sc.AllocBytes), ts(sc.FinishedAt)
					if sc.FsTotalBytes != nil {
						row[8] = i64(*sc.FsTotalBytes)
					}
					if sc.FsFreeBytes != nil {
						row[9] = i64(*sc.FsFreeBytes)
					}
				}
				return row
			})
	}
	return writeJSON(w, items[query.ShareInfo]{Items: shares})
}

func (s *Server) getShare(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	sh, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, sh)
}

func (s *Server) listScans(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	p, err := parseList(r, "", true)
	if err != nil {
		return err
	}
	before, err := parseIntParam(r, "before", 0)
	if err != nil {
		return err
	}
	page, err := s.q.Scans(r.Context(), id, int64(before), p.Limit)
	if err != nil {
		return err
	}
	return writeJSON(w, page)
}

func (s *Server) listScanErrors(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	scan, err := parseID(r.PathValue("scan"), "scan")
	if err != nil {
		return err
	}
	p, err := parseList(r, "", true)
	if err != nil {
		return err
	}
	after, err := parseIntParam(r, "after", 0)
	if err != nil {
		return err
	}
	page, err := s.q.ScanErrors(r.Context(), id, scan, r.URL.Query().Get("kind"), int64(after), p.Limit)
	if err != nil {
		return err
	}
	return writeJSON(w, page)
}

// ---- breakdowns -----------------------------------------------------------

func (s *Server) shareCategories(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	dir := int64(0)
	if v := r.URL.Query().Get("dir"); v != "" {
		if dir, err = parseDirID(v); err != nil {
			return err
		}
	}
	list, err := s.q.Categories(r.Context(), id, dir)
	if err != nil {
		return err
	}
	if csv, err := wantsCSV(r); err != nil {
		return err
	} else if csv {
		return streamList(s, w, "categories.csv", []string{"id", "key", "size", "alloc", "files", "pct"}, list,
			func(c query.CategoryTotal) []string {
				return []string{strconv.Itoa(int(c.ID)), c.Key, i64(c.Size), i64(c.Alloc), i64(c.Files), f64(c.Pct)}
			})
	}
	return writeJSON(w, items[query.CategoryTotal]{Items: list})
}

func (s *Server) shareExtensions(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	cats, err := s.parseCategories(r, "category")
	if err != nil {
		return err
	}
	p, err := parseList(r, "", true)
	if err != nil {
		return err
	}
	list, err := s.q.Extensions(r.Context(), id, cats, p.Limit)
	if err != nil {
		return err
	}
	if csv, err := wantsCSV(r); err != nil {
		return err
	} else if csv {
		return streamList(s, w, "extensions.csv", []string{"ext", "category_id", "size", "alloc", "files", "pct"}, list,
			func(e query.ExtensionTotal) []string {
				return []string{textCell(e.Ext), strconv.Itoa(int(e.CategoryID)), i64(e.Size), i64(e.Alloc), i64(e.Files), f64(e.Pct)}
			})
	}
	return writeJSON(w, items[query.ExtensionTotal]{Items: list})
}

func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) error {
	list, err := s.q.CategoryList(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, items[query.Category]{Items: list})
}

func (s *Server) listExtensionMap(w http.ResponseWriter, r *http.Request) error {
	list, err := s.q.ExtensionMap(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, items[query.ExtensionMapping]{Items: list})
}

// ---- directories ----------------------------------------------------------

func (s *Server) getDir(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	dir, err := parseDirID(r.PathValue("dir"))
	if err != nil {
		return err
	}
	f, err := s.parseFilter(r)
	if err != nil {
		return err
	}
	d, err := s.q.Dir(r.Context(), id, dir, f)
	if err != nil {
		return err
	}
	return writeJSON(w, d)
}

func (s *Server) dirChildren(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	dir, err := parseDirID(r.PathValue("dir"))
	if err != nil {
		return err
	}
	f, err := s.parseFilter(r)
	if err != nil {
		return err
	}
	p, err := parseList(r, "size", true)
	if err != nil {
		return err
	}
	cq := query.ChildQuery{ShareID: id, DirID: dir, Filter: f, Sort: p.Sort, Desc: p.Desc, Limit: p.Limit, Cursor: p.Cursor}
	csv, err := wantsCSV(r)
	if err != nil {
		return err
	}
	if !csv {
		page, err := s.q.Children(r.Context(), cq)
		if err != nil {
			return err
		}
		w.Header().Set("X-Query-Tier", strconv.Itoa(page.Tier))
		return writeJSON(w, page)
	}
	return streamPaged(s, w, "folders.csv",
		[]string{"id", "name", "size", "alloc", "files", "dirs", "pct", "oldest_mtime", "newest_mtime", "oldest_atime", "newest_atime"},
		func(cur string) ([]query.Child, string, error) {
			c := cq
			c.Limit, c.Cursor = 1000, cur
			page, err := s.q.Children(r.Context(), c)
			if err != nil {
				return nil, "", err
			}
			return page.Items, page.NextCursor, nil
		},
		func(c query.Child) []string {
			return []string{i64(c.ID), textCell(c.Name), i64(c.Size), i64(c.Alloc), i64(c.Files), i64(c.Dirs), f64(c.Pct),
				ts(c.OldestMtime), ts(c.NewestMtime), ts(c.OldestAtime), ts(c.NewestAtime)}
		})
}

func (s *Server) dirTreemap(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	dir, err := parseDirID(r.PathValue("dir"))
	if err != nil {
		return err
	}
	cats, err := s.parseCategories(r, "category")
	if err != nil {
		return err
	}
	depth, err := parseIntParam(r, "depth", 0)
	if err != nil {
		return err
	}
	maxc, err := parseIntParam(r, "max_children", 0)
	if err != nil {
		return err
	}
	tree, err := s.q.Treemap(r.Context(), id, dir, cats, depth, maxc)
	if err != nil {
		return err
	}
	return writeJSON(w, tree)
}

// ---- files ------------------------------------------------------------------

var fileHeader = []string{"id", "path", "name", "ext", "category_id", "size", "alloc", "mtime", "atime", "ctime", "nlink", "counted"}

func fileRow(f query.File) []string {
	return []string{i64(f.ID), textCell(f.Path), textCell(f.Name), textCell(f.Ext), strconv.Itoa(int(f.Category)), i64(f.Size),
		i64(f.Alloc), tv(f.Mtime), tv(f.Atime), tv(f.Ctime), strconv.Itoa(f.Nlink), strconv.FormatBool(f.Counted)}
}

// serveFiles answers a file query as JSON or CSV.
func (s *Server) serveFiles(w http.ResponseWriter, r *http.Request, fq query.FileQuery, filename string) error {
	csv, err := wantsCSV(r)
	if err != nil {
		return err
	}
	if !csv {
		page, err := s.q.Files(r.Context(), fq)
		if err != nil {
			return err
		}
		return writeJSON(w, page)
	}
	return streamPaged(s, w, filename, fileHeader, func(cur string) ([]query.File, string, error) {
		c := fq
		c.Limit, c.Cursor = 1000, cur
		page, err := s.q.Files(r.Context(), c)
		if err != nil {
			return nil, "", err
		}
		return page.Items, page.NextCursor, nil
	}, fileRow)
}

// fileScope reads the dir parameter shared by the file endpoints: absent or
// "root" means the whole share.
func fileScope(r *http.Request) (dir int64, err error) {
	if v := r.URL.Query().Get("dir"); v != "" {
		return parseDirID(v)
	}
	return 0, nil
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	f, err := s.parseFilter(r)
	if err != nil {
		return err
	}
	p, err := parseList(r, "size", true)
	if err != nil {
		return err
	}
	dir, err := fileScope(r)
	if err != nil {
		return err
	}
	// Without dir the whole share is searched; with dir only that folder unless recursive=true.
	recursive, err := parseBool(r, "recursive", r.URL.Query().Get("dir") == "")
	if err != nil {
		return err
	}
	counted, err := parseBool(r, "counted_only", false)
	if err != nil {
		return err
	}
	return s.serveFiles(w, r, query.FileQuery{ShareID: id, DirID: dir, Recursive: recursive, Filter: f, Sort: p.Sort,
		Desc: p.Desc, Limit: p.Limit, Cursor: p.Cursor, CountedOnly: counted}, "files.csv")
}

func (s *Server) topFiles(w http.ResponseWriter, r *http.Request) error {
	return s.rankedFiles(w, r, false)
}

func (s *Server) coldFiles(w http.ResponseWriter, r *http.Request) error {
	return s.rankedFiles(w, r, true)
}

// rankedFiles serves "biggest files" and "biggest files not used for a long
// time": recursive, hardlinks counted once, largest first by default.
func (s *Server) rankedFiles(w http.ResponseWriter, r *http.Request, cold bool) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	f, err := s.parseFilter(r)
	if err != nil {
		return err
	}
	if cold && f.UsedBefore == nil {
		t := s.now().AddDate(0, 0, -365)
		f.UsedBefore = &t
	}
	p, err := parseList(r, "size", true)
	if err != nil {
		return err
	}
	dir, err := fileScope(r)
	if err != nil {
		return err
	}
	name := "top-files.csv"
	if cold {
		name = "cold-files.csv"
	}
	return s.serveFiles(w, r, query.FileQuery{ShareID: id, DirID: dir, Recursive: true, Filter: f, Sort: p.Sort, Desc: p.Desc,
		Limit: p.Limit, Cursor: p.Cursor, CountedOnly: true}, name)
}

func (s *Server) topDirs(w http.ResponseWriter, r *http.Request) error {
	return s.rankedDirs(w, r, false)
}
func (s *Server) coldDirs(w http.ResponseWriter, r *http.Request) error {
	return s.rankedDirs(w, r, true)
}

func (s *Server) rankedDirs(w http.ResponseWriter, r *http.Request, cold bool) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	p, err := parseList(r, "size", true)
	if err != nil {
		return err
	}
	dir, err := fileScope(r)
	if err != nil {
		return err
	}
	minDepth, err := parseIntParam(r, "min_depth", 0)
	if err != nil {
		return err
	}
	dq := query.DirListQuery{ShareID: id, UnderDirID: dir, By: r.URL.Query().Get("by"), Sort: p.Sort, Desc: p.Desc,
		MinDepth: minDepth, Limit: p.Limit, Cursor: p.Cursor}
	name := "top-folders.csv"
	if cold {
		f, err := s.parseFilter(r)
		if err != nil {
			return err
		}
		days, err := parseIntParam(r, "unused_days", 365)
		if err != nil {
			return err
		}
		if days < 0 || days > 36500 {
			return bad("unused_days must be between 0 and 36500")
		}
		t := s.now().AddDate(0, 0, -days)
		if f.UsedBefore != nil {
			t = *f.UsedBefore
		}
		dq.UsedBefore = &t
		if dq.Maximal, err = parseBool(r, "maximal", true); err != nil {
			return err
		}
		name = "cold-folders.csv"
	}
	csv, err := wantsCSV(r)
	if err != nil {
		return err
	}
	if !csv {
		page, err := s.q.Dirs(r.Context(), dq)
		if err != nil {
			return err
		}
		return writeJSON(w, page)
	}
	return streamPaged(s, w, name,
		[]string{"id", "path", "name", "size", "alloc", "files", "dirs", "size_direct", "files_direct", "oldest_mtime", "newest_mtime", "oldest_atime", "newest_atime"},
		func(cur string) ([]query.DirRow, string, error) {
			c := dq
			c.Limit, c.Cursor = 1000, cur
			page, err := s.q.Dirs(r.Context(), c)
			if err != nil {
				return nil, "", err
			}
			return page.Items, page.NextCursor, nil
		},
		func(d query.DirRow) []string {
			return []string{i64(d.ID), textCell(d.Path), textCell(d.Name), i64(d.Size), i64(d.Alloc), i64(d.Files), i64(d.Dirs),
				i64(d.SizeDirect), i64(d.FilesDirect), ts(d.OldestMtime), ts(d.NewestMtime), ts(d.OldestAtime), ts(d.NewestAtime)}
		})
}

// ---- search -----------------------------------------------------------------

func (s *Server) search(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	var share int64
	if v := q.Get("share"); v != "" {
		var err error
		if share, err = parseID(v, "share"); err != nil {
			return err
		}
	}
	p, err := parseList(r, "", true)
	if err != nil {
		return err
	}
	hits, err := s.q.Search(r.Context(), query.SearchQuery{ShareID: share, Q: q.Get("q"), Kind: q.Get("kind"), Limit: p.Limit})
	if err != nil {
		return err
	}
	if csv, err := wantsCSV(r); err != nil {
		return err
	} else if csv {
		return streamList(s, w, "search.csv", []string{"share_id", "kind", "id", "path", "name", "size", "mtime"}, hits,
			func(h query.SearchHit) []string {
				return []string{i64(h.ShareID), h.Kind, i64(h.ID), textCell(h.Path), textCell(h.Name), i64(h.Size), ts(h.Mtime)}
			})
	}
	return writeJSON(w, items[query.SearchHit]{Items: hits})
}
