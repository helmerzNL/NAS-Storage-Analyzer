package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
)

// badRequest is returned by the parsing helpers and maps to HTTP 400.
type badRequest string

func (e badRequest) Error() string { return string(e) }

func bad(format string, a ...any) error { return badRequest(fmt.Sprintf(format, a...)) }

// multi returns all values of key, splitting comma-separated lists, so both
// ?ext=mkv&ext=mp4 and ?ext=mkv,mp4 work.
func multi(q url.Values, key string) []string {
	var out []string
	for _, v := range q[key] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func parseID(s, what string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return 0, bad("%s must be a positive integer", what)
	}
	return n, nil
}

// parseDirID accepts a directory id or "root" (the share root, id 0 inside
// the query layer).
func parseDirID(s string) (int64, error) {
	if s == "root" {
		return 0, nil
	}
	return parseID(s, "dir")
}

func parseTime(s, name string) (*time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return &t, nil
		}
	}
	return nil, bad("%s must be an RFC 3339 timestamp or a YYYY-MM-DD date", name)
}

func parseInt64Param(q url.Values, key string) (*int64, error) {
	v := q.Get(key)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return nil, bad("%s must be a non-negative integer (bytes)", key)
	}
	return &n, nil
}

// parseCategories accepts ids (1) or keys (video), comma-separated or repeated.
func (s *Server) parseCategories(r *http.Request, key string) ([]int16, error) {
	vals := multi(r.URL.Query(), key)
	if len(vals) == 0 {
		return nil, nil
	}
	keys, err := s.categoryKeys(r)
	if err != nil {
		return nil, err
	}
	var out []int16
	for _, v := range vals {
		if n, err := strconv.ParseInt(v, 10, 16); err == nil {
			if _, ok := keys[int16(n)]; !ok {
				return nil, bad("unknown category %q", v)
			}
			out = append(out, int16(n))
			continue
		}
		found := false
		for id, k := range keys {
			if strings.EqualFold(k, v) {
				out = append(out, id)
				found = true
			}
		}
		if !found {
			return nil, bad("unknown category %q", v)
		}
	}
	return out, nil
}

// parseFilter reads the file filter shared by all list endpoints.
func (s *Server) parseFilter(r *http.Request) (query.Filter, error) {
	q := r.URL.Query()
	var f query.Filter
	var err error
	if f.Categories, err = s.parseCategories(r, "category"); err != nil {
		return f, err
	}
	for _, e := range multi(q, "ext") {
		if strings.EqualFold(e, "none") {
			e = ""
		}
		f.Exts = append(f.Exts, e)
	}
	if f.MinSize, err = parseInt64Param(q, "min_size"); err != nil {
		return f, err
	}
	if f.MaxSize, err = parseInt64Param(q, "max_size"); err != nil {
		return f, err
	}
	for key, dst := range map[string]**time.Time{
		"modified_before": &f.ModifiedBefore, "modified_after": &f.ModifiedAfter,
		"used_before": &f.UsedBefore, "used_after": &f.UsedAfter,
	} {
		if v := q.Get(key); v != "" {
			if *dst, err = parseTime(v, key); err != nil {
				return f, err
			}
		}
	}
	if d := q.Get("unused_days"); d != "" {
		days, err := strconv.Atoi(d)
		if err != nil || days < 0 || days > 36500 {
			return f, bad("unused_days must be between 0 and 36500")
		}
		if f.UsedBefore == nil {
			t := s.now().AddDate(0, 0, -days)
			f.UsedBefore = &t
		}
	}
	f.Q = strings.TrimSpace(q.Get("q"))
	return f, f.Validate()
}

// listParams are the paging and ordering parameters shared by list endpoints.
type listParams struct {
	Limit  int
	Cursor string
	Sort   string
	Desc   bool
}

// parseList reads limit (alias n), cursor, sort and order. defDesc is the
// default direction for sorts that are not name-like.
func parseList(r *http.Request, defSort string, defDesc bool) (listParams, error) {
	q := r.URL.Query()
	p := listParams{Cursor: q.Get("cursor"), Sort: q.Get("sort")}
	if p.Sort == "" {
		p.Sort = defSort
	}
	lim := q.Get("limit")
	if lim == "" {
		lim = q.Get("n")
	}
	if lim != "" {
		n, err := strconv.Atoi(lim)
		if err != nil || n < 1 {
			return p, bad("limit must be a positive integer")
		}
		p.Limit = n
	}
	switch o := q.Get("order"); o {
	case "":
		p.Desc = defDesc
		if p.Sort == "name" || p.Sort == "ext" {
			p.Desc = false
		}
	case "asc":
	case "desc":
		p.Desc = true
	default:
		return p, bad("order must be asc or desc")
	}
	return p, nil
}

func wantsCSV(r *http.Request) (bool, error) {
	switch f := r.URL.Query().Get("format"); f {
	case "csv":
		return true, nil
	case "", "json":
		if f == "" && strings.Contains(r.Header.Get("Accept"), "text/csv") {
			return true, nil
		}
		return false, nil
	default:
		return false, bad("format must be json or csv")
	}
}
