package query_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanrun"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

type spec struct {
	path         string
	size         int
	mtime, atime time.Time
}

var qtree = []spec{
	{"top.txt", 10, date(2020, 1, 1), date(2025, 6, 1)},
	{"video/a.mkv", 5000, date(2019, 3, 1), date(2019, 3, 2)},
	{"video/b.mp4", 3000, date(2024, 2, 1), date(2026, 1, 5)},
	{"video/old/c.mkv", 8000, date(2015, 5, 5), date(2015, 5, 6)},
	{"video/old/d.avi", 100, date(2015, 5, 5), date(2025, 7, 7)},
	{"audio/e.flac", 2000, date(2022, 8, 8), date(2022, 8, 9)},
	{"audio/f.mp3", 500, date(2023, 9, 9), date(2026, 2, 2)},
	{"docs/g.pdf", 700, date(2021, 1, 1), date(2021, 1, 2)},
	{"docs/h.docx", 300, date(2021, 6, 1), date(2021, 6, 2)},
	{"docs/noext", 50, date(2018, 1, 1), date(2018, 1, 2)},
	{"photos/i.CR2", 9000, date(2023, 4, 4), date(2023, 4, 5)},
	{"photos/j.jpg", 400, date(2023, 5, 5), date(2025, 12, 12)},
}

type fixture struct {
	st    *store.Store
	q     *query.DB
	share int64
	root  string
	dirs  map[string]int64 // relative path -> dir id ("" = root)
}

func setup(t *testing.T) *fixture {
	t.Helper()
	st := ts.NewStore(t)
	root := t.TempDir()
	for _, s := range qtree {
		ts.Write(t, root+"/"+s.path, s.size)
	}
	if err := os.MkdirAll(root+"/docs/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(root+"/photos/j.jpg", root+"/photos/j-copy.jpg"); err != nil {
		t.Fatal(err)
	}
	for _, s := range qtree { // after the link, so the shared inode gets j.jpg's times
		if err := os.Chtimes(root+"/"+s.path, s.atime, s.mtime); err != nil {
			t.Fatal(err)
		}
	}
	sh, err := st.EnsureShare(context.Background(), "q", root, nil, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(context.Background(), st, sh, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, q: query.New(st.Pool), share: sh.ID, root: root, dirs: map[string]int64{}}
	rows, err := st.Pool.Query(context.Background(), `
		WITH RECURSIVE t AS (SELECT id, name AS p FROM dirs WHERE share_id = $1 AND parent_id IS NULL
			UNION ALL SELECT d.id, CASE WHEN t.p = '' THEN d.name ELSE t.p || '/' || d.name END
			FROM dirs d JOIN t ON d.parent_id = t.id WHERE d.share_id = $1) SELECT id, p FROM t`, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		rows.Scan(&id, &p)
		f.dirs[p] = id
	}
	return f
}

// ---- independent oracle -------------------------------------------------

type orow struct {
	id, dir      int64
	name, ext    string
	size         int64
	mtime, atime time.Time
	cat          int16
	counted      bool
}

type oracle struct {
	parent map[int64]int64 // dir -> parent (0 for root)
	names  map[int64]string
	files  []orow
	basis  string
}

func loadOracle(t *testing.T, f *fixture) *oracle {
	t.Helper()
	o := &oracle{parent: map[int64]int64{}, names: map[int64]string{}, basis: "atime"}
	ctx := context.Background()
	rows, err := f.st.Pool.Query(ctx, `SELECT id, coalesce(parent_id, 0), name FROM dirs WHERE share_id = $1`, f.share)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, p int64
		var n string
		rows.Scan(&id, &p, &n)
		o.parent[id], o.names[id] = p, n
	}
	rows.Close()
	rows, err = f.st.Pool.Query(ctx, `SELECT id, dir_id, name, ext, size, mtime, atime, category_id, counted FROM files WHERE share_id = $1`, f.share)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r orow
		rows.Scan(&r.id, &r.dir, &r.name, &r.ext, &r.size, &r.mtime, &r.atime, &r.cat, &r.counted)
		o.files = append(o.files, r)
	}
	rows.Close()
	return o
}

func (o *oracle) used(r orow) time.Time {
	switch o.basis {
	case "mtime":
		return r.mtime
	case "max":
		if r.atime.After(r.mtime) {
			return r.atime
		}
		return r.mtime
	}
	return r.atime
}

// child returns the child of dir that is an ancestor-or-self of d, or 0 when d == dir
// or d is outside dir's subtree.
func (o *oracle) child(dir, d int64) int64 {
	prev := int64(0)
	for x := d; x != 0; x = o.parent[x] {
		if x == dir {
			return prev
		}
		prev = x
	}
	return -1
}

type agg struct {
	size, files int64
	newestMtime time.Time
	newestUsed  time.Time
	hasFiles    bool
}

// children aggregates matching counted files per child of dir; total is the whole subtree.
func (o *oracle) children(dir int64, pred func(orow) bool) (map[int64]*agg, agg) {
	out := map[int64]*agg{}
	var total agg
	for _, r := range o.files {
		if !r.counted || !pred(r) {
			continue
		}
		c := o.child(dir, r.dir)
		if c == -1 {
			continue
		}
		add := func(a *agg) {
			a.size += r.size
			a.files++
			a.hasFiles = true
			if r.mtime.After(a.newestMtime) {
				a.newestMtime = r.mtime
			}
			if u := o.used(r); u.After(a.newestUsed) {
				a.newestUsed = u
			}
		}
		add(&total)
		if c != 0 {
			if out[c] == nil {
				out[c] = &agg{}
			}
			add(out[c])
		}
	}
	return out, total
}

// ---- tests ----------------------------------------------------------------

type fcase struct {
	name string
	f    query.Filter
	pred func(orow) bool
}

func i64(n int64) *int64        { return &n }
func tp(t time.Time) *time.Time { return &t }

func filterCases() []fcase {
	all := func(orow) bool { return true }
	return []fcase{
		{"none", query.Filter{}, all},
		{"video", query.Filter{Categories: []int16{1}}, func(r orow) bool { return r.cat == 1 }},
		{"video+audio", query.Filter{Categories: []int16{1, 2}}, func(r orow) bool { return r.cat == 1 || r.cat == 2 }},
		{"ext mkv", query.Filter{Exts: []string{"MKV"}}, func(r orow) bool { return r.ext == "mkv" }},
		{"no extension", query.Filter{Exts: []string{""}}, func(r orow) bool { return r.ext == "" }},
		{"min size", query.Filter{MinSize: i64(1000)}, func(r orow) bool { return r.size >= 1000 }},
		{"max size", query.Filter{MaxSize: i64(1000)}, func(r orow) bool { return r.size <= 1000 }},
		{"size range", query.Filter{MinSize: i64(500), MaxSize: i64(5000)}, func(r orow) bool { return r.size >= 500 && r.size <= 5000 }},
		{"modified before 2020-06", query.Filter{ModifiedBefore: tp(date(2020, 6, 1))}, func(r orow) bool { return r.mtime.Before(date(2020, 6, 1)) }},
		{"modified after 2022", query.Filter{ModifiedAfter: tp(date(2022, 1, 1))}, func(r orow) bool { return !r.mtime.Before(date(2022, 1, 1)) }},
		{"used before 2024", query.Filter{UsedBefore: tp(date(2024, 1, 1))}, func(r orow) bool { return r.atime.Before(date(2024, 1, 1)) }},
		{"used after 2025", query.Filter{UsedAfter: tp(date(2025, 1, 1))}, func(r orow) bool { return !r.atime.Before(date(2025, 1, 1)) }},
		{"combined", query.Filter{Categories: []int16{1}, UsedBefore: tp(date(2024, 1, 1)), MinSize: i64(1000)},
			func(r orow) bool { return r.cat == 1 && r.atime.Before(date(2024, 1, 1)) && r.size >= 1000 }},
	}
}

func TestChildrenMatchOracleForAllFiltersAndSorts(t *testing.T) {
	f := setup(t)
	o := loadOracle(t, f)
	ctx := context.Background()
	for _, dirPath := range []string{"", "video"} {
		dir := f.dirs[dirPath]
		for _, fc := range filterCases() {
			per, total := o.children(dir, fc.pred)
			for _, sortName := range []string{"size", "files", "name", "mtime", "used"} {
				for _, desc := range []bool{false, true} {
					label := dirPath + "/" + fc.name + "/" + sortName
					page, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, DirID: dir, Filter: fc.f, Sort: sortName, Desc: desc})
					if err != nil {
						t.Fatalf("%s: %v", label, err)
					}
					if page.Parent.Size != total.size || page.Parent.Files != total.files {
						t.Errorf("%s: parent totals %+v, oracle size=%d files=%d (tier %d)", label, page.Parent, total.size, total.files, page.Tier)
					}
					// Expected children, in order.
					type want struct {
						id          int64
						size, files int64
					}
					var exp []want
					for id := range o.names {
						if o.parent[id] != dir {
							continue
						}
						a := per[id]
						if a == nil {
							if fc.name != "none" { // the unfiltered view also lists empty directories
								continue
							}
							a = &agg{}
						}
						exp = append(exp, want{id, a.size, a.files})
					}
					key := func(w want) (int64, string) {
						a := per[w.id]
						if a == nil {
							a = &agg{}
						}
						switch sortName {
						case "files":
							return a.files, ""
						case "name":
							return 0, o.names[w.id]
						case "mtime":
							if a.newestMtime.IsZero() {
								return 0, ""
							}
							return a.newestMtime.UnixMicro(), ""
						case "used":
							if a.newestUsed.IsZero() {
								return 0, ""
							}
							return a.newestUsed.UnixMicro(), ""
						}
						return a.size, ""
					}
					sort.Slice(exp, func(i, j int) bool {
						ni, si := key(exp[i])
						nj, sj := key(exp[j])
						c := 0
						switch {
						case sortName == "name":
							c = strings.Compare(si, sj)
						case ni < nj:
							c = -1
						case ni > nj:
							c = 1
						}
						if c == 0 {
							c = int(exp[i].id - exp[j].id)
						}
						if desc {
							return c > 0
						}
						return c < 0
					})
					if len(page.Items) != len(exp) {
						t.Errorf("%s desc=%v: %d children (tier %d), oracle has %d", label, desc, len(page.Items), page.Tier, len(exp))
						continue
					}
					for i, c := range page.Items {
						if c.ID != exp[i].id || c.Size != exp[i].size || c.Files != exp[i].files {
							t.Errorf("%s desc=%v row %d: got id=%d size=%d files=%d, want id=%d size=%d files=%d (tier %d)",
								label, desc, i, c.ID, c.Size, c.Files, exp[i].id, exp[i].size, exp[i].files, page.Tier)
						}
					}
				}
			}
		}
	}
}

func TestChildrenTiers(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	check := func(name string, cq query.ChildQuery, tier int) {
		page, err := f.q.Children(ctx, cq)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if page.Tier != tier {
			t.Errorf("%s: tier %d, want %d", name, page.Tier, tier)
		}
	}
	check("no filter", query.ChildQuery{ShareID: f.share}, 1)
	check("category only", query.ChildQuery{ShareID: f.share, Filter: query.Filter{Categories: []int16{1}}}, 2)
	check("category sorted by mtime", query.ChildQuery{ShareID: f.share, Filter: query.Filter{Categories: []int16{1}}, Sort: "mtime"}, 3)
	check("extension", query.ChildQuery{ShareID: f.share, Filter: query.Filter{Exts: []string{"mkv"}}}, 3)
	check("forced live", query.ChildQuery{ShareID: f.share, ForceLive: true}, 3)
}

func TestTier2AndTier3AgreeForCategoryFilters(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, cats := range [][]int16{{1}, {2}, {1, 2}, {3}, {4}, {8}, {5}} {
		for _, dir := range []string{"", "video", "photos"} {
			fast, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, DirID: f.dirs[dir], Filter: query.Filter{Categories: cats}})
			if err != nil {
				t.Fatal(err)
			}
			live, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, DirID: f.dirs[dir], Filter: query.Filter{Categories: cats}, ForceLive: true})
			if err != nil {
				t.Fatal(err)
			}
			if fast.Tier != 2 || live.Tier != 3 {
				t.Fatalf("tiers %d/%d", fast.Tier, live.Tier)
			}
			if fast.Parent != live.Parent {
				t.Errorf("cats %v dir %q: parent totals %+v (tier 2) vs %+v (tier 3)", cats, dir, fast.Parent, live.Parent)
			}
			if len(fast.Items) != len(live.Items) {
				t.Errorf("cats %v dir %q: %d vs %d children", cats, dir, len(fast.Items), len(live.Items))
				continue
			}
			for i := range fast.Items {
				a, b := fast.Items[i], live.Items[i]
				if a.ID != b.ID || a.Size != b.Size || a.Files != b.Files || a.Alloc != b.Alloc {
					t.Errorf("cats %v dir %q row %d: tier2 %+v vs tier3 %+v", cats, dir, i, a, b)
				}
			}
		}
	}
}

func TestChildrenPercentages(t *testing.T) {
	f := setup(t)
	page, err := f.q.Children(context.Background(), query.ChildQuery{ShareID: f.share, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, c := range page.Items {
		if c.Pct < 0 || c.Pct > 100 {
			t.Errorf("%s pct = %v", c.Name, c.Pct)
		}
		sum += c.Pct
	}
	// top.txt (10 bytes) lives directly in the root, so children add up to slightly under 100 %.
	if sum <= 99 || sum > 100.0001 {
		t.Errorf("percentages add up to %v", sum)
	}
	if page.Items[0].Name != "video" || page.Items[0].Size != 5000+3000+8000+100 {
		t.Errorf("biggest child = %+v", page.Items[0])
	}
}

func TestChildrenPagination(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, fc := range []fcase{filterCases()[0], filterCases()[1], filterCases()[3]} {
		for _, sortName := range []string{"size", "files", "name", "mtime", "used"} {
			for _, desc := range []bool{false, true} {
				full, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, Filter: fc.f, Sort: sortName, Desc: desc})
				if err != nil {
					t.Fatal(err)
				}
				var got []int64
				cursorTok := ""
				for pages := 0; pages < 20; pages++ {
					p, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, Filter: fc.f, Sort: sortName, Desc: desc, Limit: 1, Cursor: cursorTok})
					if err != nil {
						t.Fatalf("%s/%s: %v", fc.name, sortName, err)
					}
					for _, c := range p.Items {
						got = append(got, c.ID)
					}
					if p.NextCursor == "" {
						break
					}
					cursorTok = p.NextCursor
				}
				if len(got) != len(full.Items) {
					t.Errorf("%s/%s desc=%v: paged %d rows, unpaged %d", fc.name, sortName, desc, len(got), len(full.Items))
					continue
				}
				for i := range got {
					if got[i] != full.Items[i].ID {
						t.Errorf("%s/%s desc=%v: page order differs at %d", fc.name, sortName, desc, i)
					}
				}
			}
		}
	}
}

func TestLastUsedFollowsShareBasis(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cold := func() []string {
		page, err := f.q.Dirs(ctx, query.DirListQuery{ShareID: f.share, UsedBefore: tp(date(2024, 1, 1)), Maximal: true, Desc: true})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range page.Items {
			names = append(names, d.Path)
		}
		return names
	}
	// atime: video/old holds d.avi (atime 2025) so is not entirely cold; docs, audio? audio has f.mp3 atime 2026.
	atime := strings.Join(cold(), ",")
	if _, err := f.st.Pool.Exec(ctx, `UPDATE shares SET last_used_basis = 'mtime' WHERE id = $1`, f.share); err != nil {
		t.Fatal(err)
	}
	mtime := strings.Join(cold(), ",")
	if atime == mtime {
		t.Errorf("cold directories identical for atime and mtime basis: %s", atime)
	}
	if !strings.Contains(mtime, "/video/old") {
		t.Errorf("by mtime /video/old (all 2015) must be cold: %s", mtime)
	}
	if strings.Contains(atime, "/video/old") {
		t.Errorf("by atime /video/old (d.avi read in 2025) must not be cold: %s", atime)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE shares SET last_used_basis = 'max' WHERE id = $1`, f.share); err != nil {
		t.Fatal(err)
	}
	// With max(atime, mtime) docs (all ≤ 2021) stays cold, photos (j.jpg atime 2025) does not.
	max := strings.Join(cold(), ",")
	if !strings.Contains(max, "/docs") || strings.Contains(max, "/photos") {
		t.Errorf("max basis cold dirs = %s", max)
	}
}

func TestMaximalColdDirsSkipColdChildrenOfColdParents(t *testing.T) {
	f := setup(t)
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE shares SET last_used_basis = 'mtime' WHERE id = $1`, f.share); err != nil {
		t.Fatal(err)
	}
	cutoff := tp(date(2016, 1, 1)) // only video/old (mtime 2015) is cold... plus nothing else
	all, err := f.q.Dirs(context.Background(), query.DirListQuery{ShareID: f.share, UsedBefore: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 1 || all.Items[0].Path != "/video/old" {
		t.Fatalf("cold dirs = %+v", all.Items)
	}
	cutoff = tp(date(2019, 1, 1)) // cold: video/old, docs/noext's dir? docs has 2021 files, so only old.
	later, _ := f.q.Dirs(context.Background(), query.DirListQuery{ShareID: f.share, UsedBefore: cutoff})
	if len(later.Items) != 1 {
		t.Errorf("cold dirs by 2019 = %+v", later.Items)
	}
	// A parent that is entirely cold hides its cold child when Maximal.
	cutoff = tp(date(2030, 1, 1)) // everything is cold
	every, _ := f.q.Dirs(context.Background(), query.DirListQuery{ShareID: f.share, UsedBefore: cutoff, Desc: true})
	max, _ := f.q.Dirs(context.Background(), query.DirListQuery{ShareID: f.share, UsedBefore: cutoff, Desc: true, Maximal: true})
	if len(max.Items) >= len(every.Items) {
		t.Errorf("maximal list (%d) is not shorter than the full list (%d)", len(max.Items), len(every.Items))
	}
	for _, d := range max.Items {
		if strings.Count(d.Path, "/") > 1 {
			t.Errorf("nested dir %s listed although its parent is cold", d.Path)
		}
	}
}

func TestDirListSortingAndByDirect(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	total, err := f.q.Dirs(ctx, query.DirListQuery{ShareID: f.share, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if total.Items[0].Path != "/video" || total.Items[0].Size != 16100 {
		t.Errorf("largest by total = %+v", total.Items[0])
	}
	direct, err := f.q.Dirs(ctx, query.DirListQuery{ShareID: f.share, Desc: true, By: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	// photos: 9000 + 400 direct; video/old: 8100 direct; video itself only 8000.
	if direct.Items[0].Path != "/photos" || direct.Items[1].Path != "/video/old" {
		t.Errorf("largest by direct = %v, %v", direct.Items[0].Path, direct.Items[1].Path)
	}
	// Pagination over the directory list.
	var paged []string
	cur := ""
	for i := 0; i < 20; i++ {
		p, err := f.q.Dirs(ctx, query.DirListQuery{ShareID: f.share, Desc: true, Limit: 2, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range p.Items {
			paged = append(paged, d.Path)
		}
		if cur = p.NextCursor; cur == "" {
			break
		}
	}
	if len(paged) != len(total.Items) {
		t.Fatalf("paged %d dirs, unpaged %d", len(paged), len(total.Items))
	}
	for i := range paged {
		if paged[i] != total.Items[i].Path {
			t.Errorf("paged order differs at %d: %s vs %s", i, paged[i], total.Items[i].Path)
		}
	}
	// Under a directory.
	under, err := f.q.Dirs(ctx, query.DirListQuery{ShareID: f.share, UnderDirID: f.dirs["video"], Desc: true})
	if err != nil || len(under.Items) != 1 || under.Items[0].Path != "/video/old" {
		t.Errorf("under video = %+v err=%v", under, err)
	}
}

func TestFilesFiltersSortsAndPagination(t *testing.T) {
	f := setup(t)
	o := loadOracle(t, f)
	ctx := context.Background()
	for _, fc := range filterCases() {
		for _, sortName := range []string{"size", "name", "mtime", "used", "ext"} {
			for _, desc := range []bool{true, false} {
				fq := query.FileQuery{ShareID: f.share, Recursive: true, Filter: fc.f, Sort: sortName, Desc: desc, CountedOnly: true}
				full, err := f.q.Files(ctx, fq)
				if err != nil {
					t.Fatalf("%s/%s: %v", fc.name, sortName, err)
				}
				want := 0
				for _, r := range o.files {
					if r.counted && fc.pred(r) {
						want++
					}
				}
				if len(full.Items) != want {
					t.Errorf("%s/%s: %d files, oracle %d", fc.name, sortName, len(full.Items), want)
				}
				// Page through 3 at a time and compare with the unpaged order.
				fq.Limit = 3
				var ids []int64
				for pages := 0; pages < 30; pages++ {
					p, err := f.q.Files(ctx, fq)
					if err != nil {
						t.Fatal(err)
					}
					for _, it := range p.Items {
						ids = append(ids, it.ID)
					}
					if p.NextCursor == "" {
						break
					}
					fq.Cursor = p.NextCursor
				}
				if len(ids) != len(full.Items) {
					t.Errorf("%s/%s: paged %d, unpaged %d", fc.name, sortName, len(ids), len(full.Items))
					continue
				}
				for i := range ids {
					if ids[i] != full.Items[i].ID {
						t.Errorf("%s/%s desc=%v: paging order differs at %d", fc.name, sortName, desc, i)
						break
					}
				}
			}
		}
	}
}

func TestFilesScopeAndPaths(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	list := func(fq query.FileQuery) []string {
		fq.ShareID = f.share
		fq.Sort, fq.Desc = "size", true
		p, err := f.q.Files(ctx, fq)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range p.Items {
			out = append(out, it.Path)
		}
		return out
	}
	if got := list(query.FileQuery{DirID: f.dirs["video"]}); strings.Join(got, ",") != "/video/a.mkv,/video/b.mp4" {
		t.Errorf("video non-recursive = %v", got)
	}
	if got := list(query.FileQuery{DirID: f.dirs["video"], Recursive: true}); len(got) != 4 || got[0] != "/video/old/c.mkv" {
		t.Errorf("video recursive = %v", got)
	}
	if got := list(query.FileQuery{}); strings.Join(got, ",") != "/top.txt" {
		t.Errorf("root non-recursive = %v", got)
	}
	if got := list(query.FileQuery{Recursive: true}); len(got) != 13 { // 12 counted files + the second hardlink j-copy.jpg, listed because CountedOnly is false
		t.Errorf("whole share = %d files: %v", len(got), got)
	}
	if got := list(query.FileQuery{Recursive: true, CountedOnly: true}); len(got) != 12 {
		t.Errorf("counted only = %d files", len(got))
	}
	// Name search inside the file list, case-insensitive and with LIKE wildcards escaped.
	if got := list(query.FileQuery{Recursive: true, Filter: query.Filter{Q: "J-COPY"}}); len(got) != 1 || got[0] != "/photos/j-copy.jpg" {
		t.Errorf("q=J-COPY -> %v", got)
	}
	if got := list(query.FileQuery{Recursive: true, Filter: query.Filter{Q: "%"}}); len(got) != 0 {
		t.Errorf("q=%% must match literally, got %v", got)
	}
}

func TestDirDetail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.q.Dir(ctx, f.share, f.dirs["video/old"], query.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != "/video/old" || len(d.Breadcrumb) != 3 || d.Breadcrumb[0].Name != "" || d.Breadcrumb[1].Name != "video" {
		t.Errorf("path/breadcrumb = %q %+v", d.Path, d.Breadcrumb)
	}
	if d.Total.Size != 8100 || d.Direct.Size != 8100 || d.Total.Files != 2 || d.Matching != nil {
		t.Errorf("detail = %+v", d)
	}
	if len(d.Categories) != 1 || d.Categories[0].Key != "video" || d.Categories[0].Pct != 100 {
		t.Errorf("categories = %+v", d.Categories)
	}
	d, err = f.q.Dir(ctx, f.share, 0, query.Filter{Exts: []string{"mkv"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != "" || d.Matching == nil || d.Matching.Size != 13000 || d.Matching.Files != 2 {
		t.Errorf("root with filter: path=%q matching=%+v", d.Path, d.Matching)
	}
}

func TestTreemapAddsUp(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, cats := range [][]int16{nil, {1}, {1, 3}} {
		tree, err := f.q.Treemap(ctx, f.share, 0, cats, 3, 2)
		if err != nil {
			t.Fatal(err)
		}
		var walk func(n query.TreeNode)
		walk = func(n query.TreeNode) {
			if len(n.Children) == 0 {
				return
			}
			var sum int64
			for _, c := range n.Children {
				sum += c.Size
				walk(c)
			}
			if sum != n.Size {
				t.Errorf("cats %v: children of %q add up to %d, node is %d", cats, n.Name, sum, n.Size)
			}
		}
		walk(*tree)
		if tree.Size == 0 {
			t.Errorf("cats %v: empty treemap", cats)
		}
	}
	// max_children=1 folds the rest into "other".
	tree, _ := f.q.Treemap(ctx, f.share, 0, nil, 1, 1)
	kinds := map[string]bool{}
	for _, c := range tree.Children {
		kinds[c.Kind] = true
	}
	if !kinds["dir"] || !kinds["other"] || !kinds["files"] {
		t.Errorf("root children kinds = %v", kinds)
	}
}

func TestCategoriesAndExtensions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cats, err := f.q.Categories(ctx, f.share, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cats[0].Key != "video" || cats[0].Size != 16100 {
		t.Errorf("categories = %+v", cats)
	}
	var pct float64
	for _, c := range cats {
		pct += c.Pct
	}
	if pct < 99.99 || pct > 100.01 {
		t.Errorf("category percentages add up to %v", pct)
	}
	exts, err := f.q.Extensions(ctx, f.share, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 3 || exts[0].Ext != "mkv" || exts[0].Size != 13000 {
		t.Errorf("extensions = %+v", exts)
	}
	imgs, _ := f.q.Extensions(ctx, f.share, []int16{3}, 10)
	for _, e := range imgs {
		if e.CategoryID != 3 {
			t.Errorf("extension %s has category %d", e.Ext, e.CategoryID)
		}
	}
}

func TestSearch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, err := f.q.Search(ctx, query.SearchQuery{ShareID: f.share, Q: "OLD"})
	if err != nil {
		t.Fatal(err)
	}
	if hits := res.Items; len(hits) != 1 || hits[0].Kind != "dir" || hits[0].Path != "/video/old" || res.Truncated {
		t.Errorf("hits = %+v truncated=%v", hits, res.Truncated)
	}
	res, _ = f.q.Search(ctx, query.SearchQuery{Q: ".mkv"})
	if hits := res.Items; len(hits) != 2 || hits[0].Name != "c.mkv" || hits[0].Path != "/video/old/c.mkv" {
		t.Errorf("mkv hits = %+v", hits)
	}
	if _, err := f.q.Search(ctx, query.SearchQuery{Q: "ab"}); !isInvalid(err) {
		t.Errorf("short search err = %v", err)
	}
}

func isInvalid(err error) bool {
	var ie *query.InvalidError
	return errors.As(err, &ie)
}

func TestSharesScansAndErrors(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	shares, err := f.q.Shares(ctx)
	if err != nil || len(shares) != 1 {
		t.Fatalf("shares = %v err=%v", shares, err)
	}
	s := shares[0]
	if s.Name != "q" || s.LastScan == nil || s.LastScan.Files != 12 || s.RootDirID == nil || *s.RootDirID != f.dirs[""] {
		t.Errorf("share = %+v last=%+v", s, s.LastScan)
	}
	if s.LatestScan == nil || s.LatestScan.ID != s.LastScan.ID || s.LastScan.FsTotalBytes == nil {
		t.Errorf("latest scan = %+v", s.LatestScan)
	}
	pg, err := f.q.Scans(ctx, f.share, 0, 10)
	if err != nil || len(pg.Items) != 1 {
		t.Errorf("scans = %+v err=%v", pg, err)
	}
	ep, err := f.q.ScanErrors(ctx, f.share, s.LastScan.ID, "", 0, 10)
	if err != nil || len(ep.Items) != 0 {
		t.Errorf("scan errors = %+v err=%v", ep, err)
	}
	if _, err := f.q.ScanErrors(ctx, f.share, 99999, "", 0, 10); !errors.Is(err, query.ErrNotFound) {
		t.Errorf("unknown scan err = %v", err)
	}
}

func TestErrorsAndEdgeCases(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.q.Children(ctx, query.ChildQuery{ShareID: 9999}); !errors.Is(err, query.ErrNotFound) {
		t.Errorf("unknown share: %v", err)
	}
	if _, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, DirID: 9999999}); !errors.Is(err, query.ErrNotFound) {
		t.Errorf("unknown dir: %v", err)
	}
	// A directory id of another share must not resolve.
	other, _ := f.st.EnsureShare(ctx, "other", t.TempDir(), nil, 1, 0)
	if _, err := f.q.Children(ctx, query.ChildQuery{ShareID: other.ID}); !errors.Is(err, query.ErrNoData) {
		t.Errorf("unscanned share: %v", err)
	}
	for name, cq := range map[string]query.ChildQuery{
		"bad sort":      {ShareID: f.share, Sort: "colour"},
		"bad limit":     {ShareID: f.share, Limit: -1},
		"huge limit":    {ShareID: f.share, Limit: 100000},
		"bad cursor":    {ShareID: f.share, Cursor: "!!!"},
		"min>max":       {ShareID: f.share, Filter: query.Filter{MinSize: i64(10), MaxSize: i64(1)}},
		"negative size": {ShareID: f.share, Filter: query.Filter{MinSize: i64(-1)}},
	} {
		if _, err := f.q.Children(ctx, cq); !isInvalid(err) {
			t.Errorf("%s: err = %v, want InvalidError", name, err)
		}
	}
	// A cursor from another sort order is rejected.
	p, _ := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, Limit: 1, Sort: "size"})
	if _, err := f.q.Children(ctx, query.ChildQuery{ShareID: f.share, Sort: "name", Cursor: p.NextCursor}); !isInvalid(err) {
		t.Errorf("cursor reuse across sorts: %v", err)
	}
}

func TestSearchCommonTermsAndExtensions(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	for i := 1; i <= 2300; i++ {
		ts.Write(t, root+"/bulk/common_"+strings.Repeat("0", 4-len(itoa(i)))+itoa(i)+".txt", i)
	}
	ts.Write(t, root+"/bulk/other.bin", 5000)
	ts.Write(t, root+"/bulk/BIG.TXT", 9999)
	sh, err := st.EnsureShare(context.Background(), "common", root, nil, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(context.Background(), st, sh, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	q := query.New(st.Pool)
	search := func(term string, limit int) *query.SearchResult {
		t.Helper()
		res, err := q.Search(context.Background(), query.SearchQuery{ShareID: sh.ID, Q: term, Kind: "files", Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Fewer matches than the candidate bound: exact, largest first.
	res := search("COMMON_", 5)
	if res.Truncated || len(res.Items) != 5 {
		t.Fatalf("truncated=%v items=%d", res.Truncated, len(res.Items))
	}
	for i, h := range res.Items {
		if want := int64(2300 - i); h.Size != want {
			t.Errorf("hit %d size = %d, want %d", i, h.Size, want)
		}
	}
	// A term that looks like an extension is exact through the extension index,
	// whatever the number of matches, and case-insensitive.
	defer query.SetSearchCandidates(100)()
	res = search(".TXT", 3)
	if res.Truncated || len(res.Items) != 3 || res.Items[0].Name != "BIG.TXT" || res.Items[1].Size != 2300 {
		t.Errorf(".TXT: truncated=%v items=%+v", res.Truncated, res.Items)
	}
	// Any other term with more matches than the bound is truncated, and says so.
	res = search("common_", 5)
	if !res.Truncated || len(res.Items) != 5 {
		t.Fatalf("truncated=%v items=%d, want truncated with 5 items", res.Truncated, len(res.Items))
	}
	for i := 1; i < len(res.Items); i++ {
		if res.Items[i].Size > res.Items[i-1].Size {
			t.Errorf("truncated result is not sorted by size: %v", res.Items)
		}
	}
	// A rare term still returns exactly what matches.
	if res = search("common_0042", 10); res.Truncated || len(res.Items) != 1 || res.Items[0].Size != 42 {
		t.Errorf("rare term: %+v", res)
	}
}

func itoa(n int) string { return fmt.Sprint(n) }
