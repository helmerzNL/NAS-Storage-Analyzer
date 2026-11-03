package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	apispec "github.com/helmerznl/nas-storage-analyzer/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/buildinfo"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanrun"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

type fileSpec struct {
	path         string
	size         int
	mtime, atime time.Time
}

var tree = []fileSpec{
	{"top.txt", 10, date(2020, 1, 1), date(2025, 6, 1)},
	{"video/a.mkv", 5000, date(2019, 3, 1), date(2019, 3, 2)},
	{"video/b.mp4", 3000, date(2024, 2, 1), date(2026, 1, 5)},
	{"video/old/c.mkv", 8000, date(2015, 5, 5), date(2015, 5, 6)},
	{"audio/e.flac", 2000, date(2022, 8, 8), date(2022, 8, 9)},
	{"docs/g.pdf", 700, date(2021, 1, 1), date(2021, 1, 2)},
	{"docs/noext", 50, date(2018, 1, 1), date(2018, 1, 2)},
	{"photos/i.CR2", 9000, date(2023, 4, 4), date(2023, 4, 5)},
	{"formulas/=HYPERLINK.txt", 5, date(2020, 1, 1), date(2020, 1, 1)},
	{"formulas/+plus.txt", 6, date(2020, 1, 1), date(2020, 1, 1)},
	{"formulas/@at.txt", 7, date(2020, 1, 1), date(2020, 1, 1)},
	{"formulas/-minus.txt", 8, date(2020, 1, 1), date(2020, 1, 1)},
}

var fixedNow = date(2026, 6, 1)

type fixture struct {
	st    *store.Store
	srv   *api.Server
	share int64
	root  string
}

func newFixture(t *testing.T, cfg api.Config) *fixture {
	t.Helper()
	st := ts.NewStore(t)
	root := t.TempDir()
	for _, s := range tree {
		ts.Write(t, root+"/"+s.path, s.size)
		if err := os.Chtimes(root+"/"+s.path, s.atime, s.mtime); err != nil {
			t.Fatal(err)
		}
	}
	return finishFixture(t, st, root, cfg)
}

func finishFixture(t *testing.T, st *store.Store, root string, cfg api.Config) *fixture {
	t.Helper()
	sh, err := st.EnsureShare(context.Background(), "q", root, nil, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(context.Background(), st, sh, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(context.Background(), st.Pool.Config().ConnString(), 5, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ro.Close)
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return fixedNow }
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{st: st, srv: api.New(ro, log, cfg), share: sh.ID, root: root}
}

func (f *fixture) get(t *testing.T, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) json(t *testing.T, path string, wantStatus int, into any) {
	t.Helper()
	rec := f.get(t, path)
	if rec.Code != wantStatus {
		t.Fatalf("GET %s = %d, want %d: %s", path, rec.Code, wantStatus, rec.Body)
	}
	if into != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("GET %s: %v\n%s", path, err, rec.Body)
		}
	}
}

func (f *fixture) rootDir(t *testing.T) int64 {
	var sh struct {
		RootDirID int64 `json:"root_dir_id"`
	}
	f.json(t, fmt.Sprintf("/api/v1/shares/%d", f.share), 200, &sh)
	return sh.RootDirID
}

// ---- contract ---------------------------------------------------------------

func loadSpec(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return doc, router
}

func TestResponsesMatchOpenAPIContract(t *testing.T) {
	f := newFixture(t, api.Config{})
	_, router := loadSpec(t)
	ctx := context.Background()
	s := fmt.Sprintf("/api/v1/shares/%d", f.share)
	root := f.rootDir(t)
	rootDir := fmt.Sprintf("%s/dirs/%d", s, root)

	type tc struct {
		path   string
		status int
	}
	cases := []tc{
		{"/healthz", 200}, {"/readyz", 200}, {"/openapi.yaml", 200}, {"/metrics", 200}, {"/api/v1/version", 200},
		{"/api/v1/shares", 200}, {s, 200}, {s + "/scans", 200}, {s + "/scans?limit=1", 200},
		{s + "/categories", 200}, {s + "/categories?dir=root", 200},
		{s + "/extensions", 200}, {s + "/extensions?category=video&limit=2", 200},
		{rootDir, 200}, {s + "/dirs/root", 200}, {rootDir + "?category=video", 200}, {rootDir + "?ext=mkv&min_size=100", 200},
		{rootDir + "/children", 200}, {rootDir + "/children?category=video", 200},
		{rootDir + "/children?ext=mkv&sort=used&order=asc", 200},
		{rootDir + "/children?unused_days=365&sort=name", 200},
		{rootDir + "/children?limit=1", 200},
		{rootDir + "/treemap", 200}, {rootDir + "/treemap?category=video,audio&depth=3&max_children=2", 200},
		{s + "/files", 200}, {s + "/files?dir=root", 200}, {s + "/files?dir=root&recursive=true&sort=name", 200},
		{s + "/files?q=mkv&ext=mkv&used_before=2024-01-01", 200}, {s + "/files?limit=2", 200},
		{s + "/top/files?n=3", 200}, {s + "/top/files?category=images", 200},
		{s + "/cold/files", 200}, {s + "/cold/files?unused_days=30&limit=2", 200},
		{s + "/top/dirs", 200}, {s + "/top/dirs?by=direct&min_depth=0", 200}, {s + "/top/dirs?sort=used&order=asc", 200},
		{s + "/cold/dirs", 200}, {s + "/cold/dirs?unused_days=3000&maximal=false", 200},
		{"/api/v1/search?q=mkv", 200}, {"/api/v1/search?q=old&kind=dirs&share=" + fmt.Sprint(f.share), 200},
		{"/api/v1/categories", 200}, {"/api/v1/extension-map", 200},
		// Errors are part of the contract too.
		{"/api/v1/shares/abc", 400}, {"/api/v1/shares/9999", 404}, {s + "/dirs/99999999", 404},
		{s + "/dirs/0", 400}, {rootDir + "/children?sort=colour", 400}, {rootDir + "/children?limit=0", 400},
		{rootDir + "/children?category=nope", 400}, {rootDir + "/children?min_size=-1", 400},
		{rootDir + "/children?min_size=10&max_size=1", 400}, {rootDir + "/children?used_before=yesterday", 400},
		{rootDir + "/children?cursor=garbage", 400}, {s + "/files?format=xml", 400}, {"/api/v1/search?q=ab", 400},
		{s + "/scans/99999/errors", 404}, {s + "/top/dirs?by=sideways", 400},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		route, pathParams, err := router.FindRoute(req)
		if err != nil {
			t.Errorf("%s: no route in the OpenAPI spec: %v", c.path, err)
			continue
		}
		seen[route.Path] = true
		in := &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
		if c.status < 400 {
			if err := openapi3filter.ValidateRequest(ctx, in); err != nil {
				t.Errorf("%s: request violates the spec: %v", c.path, err)
			}
		}
		rec := f.get(t, c.path)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.path, rec.Code, c.status, rec.Body)
			continue
		}
		body := rec.Body.Bytes()
		if !strings.HasPrefix(c.path, "/api/v1/") {
			continue // plain-text and YAML operational endpoints: status only
		}
		err = openapi3filter.ValidateResponse(ctx, &openapi3filter.ResponseValidationInput{
			RequestValidationInput: in, Status: rec.Code, Header: rec.Header(), Body: io.NopCloser(bytes.NewReader(body)),
			Options: &openapi3filter.Options{IncludeResponseStatus: true},
		})
		if err != nil {
			t.Errorf("%s: response violates the spec: %v\n%.600s", c.path, err, body)
		}
		if c.status >= 400 && !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("%s: error content type %q", c.path, rec.Header().Get("Content-Type"))
		}
	}
	var sh struct {
		LastScan struct{ ID int64 } `json:"last_scan"`
	}
	f.json(t, s, 200, &sh)
	errPath := fmt.Sprintf("%s/scans/%d/errors", s, sh.LastScan.ID)
	req := httptest.NewRequest("GET", errPath, nil)
	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.get(t, errPath)
	if rec.Code != 200 {
		t.Fatalf("scan errors = %d: %s", rec.Code, rec.Body)
	}
	if err := openapi3filter.ValidateResponse(ctx, &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route},
		Status:                 200, Header: rec.Header(), Body: io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}); err != nil {
		t.Errorf("scan errors response violates the spec: %v", err)
	}
}

func TestMutatingMethodsAreNotRoutedWithoutAuthentication(t *testing.T) {
	f := newFixture(t, api.Config{})
	for _, p := range []string{"/api/v1/shares", "/api/v1/users", "/api/v1/shares/1/scan"} {
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d", p, rec.Code)
		}
	}
}

// ---- behaviour ----------------------------------------------------------------

func TestChildrenEndpointReportsTierAndSorts(t *testing.T) {
	f := newFixture(t, api.Config{})
	s := fmt.Sprintf("/api/v1/shares/%d/dirs/root/children", f.share)
	cases := map[string]string{s: "1", s + "?category=video": "2", s + "?ext=mkv": "3", s + "?category=video&sort=mtime": "3"}
	for path, tier := range cases {
		rec := f.get(t, path)
		if rec.Code != 200 || rec.Header().Get("X-Query-Tier") != tier {
			t.Errorf("%s: status %d tier %q, want %s", path, rec.Code, rec.Header().Get("X-Query-Tier"), tier)
		}
	}
	var page struct {
		Items []struct {
			Name string
			Size int64
		}
	}
	f.json(t, s, 200, &page) // default order: size descending
	if len(page.Items) == 0 || page.Items[0].Name != "video" {
		t.Errorf("default order = %+v", page.Items)
	}
	f.json(t, s+"?sort=name", 200, &page) // names ascend by default
	if page.Items[0].Name != "audio" {
		t.Errorf("sort=name first = %s", page.Items[0].Name)
	}
	f.json(t, s+"?sort=name&order=desc", 200, &page)
	if page.Items[0].Name != "video" {
		t.Errorf("sort=name desc first = %s", page.Items[0].Name)
	}
}

func TestFilterParameterForms(t *testing.T) {
	f := newFixture(t, api.Config{})
	s := fmt.Sprintf("/api/v1/shares/%d/files?recursive=true&limit=1000", f.share)
	count := func(q string) int {
		var p struct{ Items []struct{ Name string } }
		f.json(t, s+q, 200, &p)
		return len(p.Items)
	}
	if got := count("&category=video"); got != 3 {
		t.Errorf("category by key = %d", got)
	}
	if got := count("&category=1"); got != 3 {
		t.Errorf("category by id = %d", got)
	}
	if a, b := count("&ext=mkv,mp4"), count("&ext=mkv&ext=mp4"); a != 3 || b != 3 {
		t.Errorf("comma/repeated ext = %d/%d", a, b)
	}
	if got := count("&ext=none"); got != 1 { // docs/noext
		t.Errorf("ext=none = %d", got)
	}
	// fixedNow is 2026-06-01: unused_days=365 -> used before 2025-06-01.
	if got := count("&unused_days=365"); got != 10 {
		t.Errorf("unused_days=365 = %d, want 10", got)
	}
	if a, b := count("&used_before=2025-06-01"), count("&used_before=2025-06-01T00:00:00Z"); a != b || a != 10 {
		t.Errorf("date forms = %d / %d", a, b)
	}
	if got := count("&q=%3D"); got != 1 { // "=" matches only =HYPERLINK.txt
		t.Errorf("q== -> %d", got)
	}
}

func TestColdEndpointsDefaultToOneYear(t *testing.T) {
	f := newFixture(t, api.Config{})
	s := fmt.Sprintf("/api/v1/shares/%d", f.share)
	var files struct{ Items []struct{ Path string } }
	f.json(t, s+"/cold/files", 200, &files)
	for _, it := range files.Items {
		if it.Path == "/video/b.mp4" { // atime 2026-01
			t.Errorf("recently used file in the cold list: %s", it.Path)
		}
	}
	if len(files.Items) == 0 || files.Items[0].Path != "/photos/i.CR2" {
		t.Errorf("cold files = %+v", files.Items)
	}
	var dirs struct{ Items []struct{ Path string } }
	f.json(t, s+"/cold/dirs", 200, &dirs)
	if len(dirs.Items) == 0 {
		t.Fatal("no cold directories")
	}
	for _, d := range dirs.Items {
		if d.Path == "/video" {
			t.Errorf("video holds b.mp4 (atime 2026) and cannot be cold: %s", d.Path)
		}
	}
}

func TestCSVExportMatchesJSONAndNeutralisesFormulas(t *testing.T) {
	f := newFixture(t, api.Config{})
	s := fmt.Sprintf("/api/v1/shares/%d", f.share)

	rec := f.get(t, s+"/files?recursive=true&format=csv&sort=name")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "files.csv") {
		t.Fatalf("csv response: %d %v", rec.Code, rec.Header())
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var j struct{ Items []struct{ Name string } }
	f.json(t, s+"/files?recursive=true&sort=name&limit=1000", 200, &j)
	if len(rows)-1 != len(j.Items) {
		t.Fatalf("csv has %d data rows, json has %d", len(rows)-1, len(j.Items))
	}
	header := rows[0]
	col := func(name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		t.Fatalf("no column %s in %v", name, header)
		return -1
	}
	names := map[string]bool{}
	for _, r := range rows[1:] {
		names[r[col("name")]] = true
		for _, c := range r {
			if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
				t.Errorf("cell %q could be executed as a formula", c)
			}
		}
	}
	for _, want := range []string{"'=HYPERLINK.txt", "'+plus.txt", "'@at.txt", "'-minus.txt", "top.txt"} {
		if !names[want] {
			t.Errorf("csv lacks name cell %q; got %v", want, names)
		}
	}
	// Same via the Accept header.
	rec = f.get(t, s+"/top/files?n=2", "Accept", "text/csv")
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Errorf("Accept: text/csv ignored: %s", rec.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/shares?format=csv", "/shares/" + fmt.Sprint(f.share) + "/dirs/root/children?format=csv",
		"/shares/" + fmt.Sprint(f.share) + "/top/dirs?format=csv", "/shares/" + fmt.Sprint(f.share) + "/cold/dirs?format=csv",
		"/shares/" + fmt.Sprint(f.share) + "/categories?format=csv", "/shares/" + fmt.Sprint(f.share) + "/extensions?format=csv",
		"/search?q=mkv&format=csv"} {
		rec := f.get(t, "/api/v1"+p)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Header().Get("Content-Type"))
			continue
		}
		if r, err := csv.NewReader(rec.Body).ReadAll(); err != nil || len(r) < 2 {
			t.Errorf("%s: %d rows, err=%v", p, len(r), err)
		}
	}
}

func TestCSVExportPagesThroughEverythingAndHonoursRowLimit(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	for i := 0; i < 2300; i++ { // more than two export pages of 1000
		ts.Write(t, fmt.Sprintf("%s/bulk/f%04d.txt", root, i), i%7)
	}
	f := finishFixture(t, st, root, api.Config{CSVMaxRows: 2000})
	s := fmt.Sprintf("/api/v1/shares/%d/files?format=csv&sort=name", f.share)
	rows, err := csv.NewReader(f.get(t, s).Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows)-1 != 2000 {
		t.Errorf("limited export has %d rows, want 2000", len(rows)-1)
	}
	seen := map[string]bool{}
	for _, r := range rows[1:] {
		if seen[r[0]] {
			t.Fatalf("row %s exported twice", r[0])
		}
		seen[r[0]] = true
	}
	f2 := finishFixture(t, ts.NewStore(t), root, api.Config{})
	rows, _ = csv.NewReader(f2.get(t, fmt.Sprintf("/api/v1/shares/%d/files?format=csv", f2.share)).Body).ReadAll()
	if len(rows)-1 != 2300 {
		t.Errorf("full export has %d rows, want 2300", len(rows)-1)
	}
}

func TestSlowQueryBecomes503(t *testing.T) {
	f := newFixture(t, api.Config{QueryTimeout: time.Nanosecond})
	rec := f.get(t, fmt.Sprintf("/api/v1/shares/%d/files?recursive=true", f.share))
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("status %d retry-after %q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	var p map[string]any
	json.Unmarshal(rec.Body.Bytes(), &p)
	if p["title"] != "query took too long" || p["request_id"] == "" {
		t.Errorf("problem = %v", p)
	}
}

func TestShareWithoutScanIs409(t *testing.T) {
	f := newFixture(t, api.Config{})
	sh, err := f.st.EnsureShare(context.Background(), "empty", t.TempDir(), nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.get(t, fmt.Sprintf("/api/v1/shares/%d/dirs/root/children", sh.ID))
	if rec.Code != 409 {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
	if rec = f.get(t, fmt.Sprintf("/api/v1/shares/%d", sh.ID)); rec.Code != 200 {
		t.Errorf("an unscanned share must still be listed, got %d", rec.Code)
	}
}

func TestReadOnlyPoolCannotWrite(t *testing.T) {
	f := newFixture(t, api.Config{})
	ro, err := store.OpenReadOnly(context.Background(), f.st.Pool.Config().ConnString(), 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec(context.Background(), `DELETE FROM shares`); err == nil {
		t.Error("write through the API pool succeeded")
	}
	var n int
	if err := ro.QueryRow(context.Background(), `SELECT count(*) FROM shares`).Scan(&n); err != nil || n != 1 {
		t.Errorf("read through the API pool: n=%d err=%v", n, err)
	}
	if _, err := ro.Exec(context.Background(), `SELECT pg_sleep(3)`); err == nil {
		t.Error("statement_timeout of 1s did not stop pg_sleep(3)")
	}
}

func TestOperationalEndpoints(t *testing.T) {
	f := newFixture(t, api.Config{})
	f.get(t, "/api/v1/shares") // generate a request metric
	f.get(t, fmt.Sprintf("/api/v1/shares/%d/dirs/root/children", f.share))

	rec := f.get(t, "/openapi.yaml")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), apispec.Spec) {
		t.Error("/openapi.yaml does not serve the embedded spec")
	}
	rec = f.get(t, "/metrics")
	body := rec.Body.String()
	for _, want := range []string{
		`nasa_share_size_bytes{share="q"} 27786`,
		`nasa_share_files{share="q"} 12`,
		`nasa_share_scan_running{share="q"} 0`,
		`nasa_share_last_scan_success{share="q"} 1`,
		`nasa_share_filesystem_free_bytes{share="q"}`,
		`nasa_http_request_duration_seconds_count{method="GET",route="GET /api/v1/shares",status_class="2xx"} 1`,
		`nasa_db_pool_max_connections`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	rec = f.get(t, "/api/v1/shares", "X-Request-Id", "abc123")
	if rec.Header().Get("X-Request-Id") != "abc123" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", rec.Header())
	}
	if f.get(t, "/api/v1/shares").Header().Get("X-Request-Id") == "" {
		t.Error("no generated request id")
	}
}

// A beta build says so (baseline section 20): the same binary reports "beta" for a beta tag and "stable"
// without one, and the endpoint is public.
func TestVersionReportsTheReleaseChannel(t *testing.T) {
	get := func(cfg api.Config) buildinfo.Info {
		t.Helper()
		var out buildinfo.Info
		newFixture(t, cfg).json(t, "/api/v1/version", 200, &out)
		return out
	}
	stable := get(api.Config{Build: buildinfo.New("0.1.0", "abc1234", "")})
	beta := get(api.Config{Build: buildinfo.New("0.1.0", "abc1234", "beta")})
	if stable != (buildinfo.Info{Version: "0.1.0", Commit: "abc1234", Channel: "stable"}) {
		t.Errorf("stable = %+v", stable)
	}
	if beta != (buildinfo.Info{Version: "0.1.0", Commit: "abc1234", Channel: "beta"}) {
		t.Errorf("beta = %+v", beta)
	}
	if def := get(api.Config{}); def.Channel != "stable" || def.Version != "dev" {
		t.Errorf("a server built without build info reports %+v, want dev/stable", def)
	}
}

func TestCheckListenAddr(t *testing.T) {
	ok := []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080", "127.0.0.2:1"}
	bad := []string{":8080", "0.0.0.0:8080", "192.168.1.5:8080", "[::]:8080", "nas.local:8080", "8080", ""}
	for _, a := range ok {
		if err := api.CheckListenAddr(a, false); err != nil {
			t.Errorf("%q should be allowed: %v", a, err)
		}
	}
	for _, a := range bad {
		if err := api.CheckListenAddr(a, false); err == nil {
			t.Errorf("%q must be refused without the override", a)
		}
	}
	if err := api.CheckListenAddr("0.0.0.0:8080", true); err != nil {
		t.Errorf("override ignored: %v", err)
	}
	if err := api.CheckListenAddr("garbage", true); err == nil {
		t.Error("a malformed address must fail even with the override")
	}
	_ = url.Values{}
}
