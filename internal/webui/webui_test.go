package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestServesFilesAndFallsBackToIndex(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	})
	if r := serve(h, "GET", "/"); r.Code != 200 || !strings.Contains(r.Body.String(), "app") || r.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("/: %d %q %v", r.Code, r.Body, r.Header())
	}
	// A deep link of the single-page app gets the index.
	if r := serve(h, "GET", "/browse/3/42"); r.Code != 200 || !strings.Contains(r.Body.String(), "app") {
		t.Errorf("deep link: %d", r.Code)
	}
	if r := serve(h, "GET", "/assets/app-1.js"); r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", r.Code, r.Header())
	}
	// A missing asset is a 404, not the HTML page served as JavaScript.
	if r := serve(h, "GET", "/assets/gone.js"); r.Code != 404 {
		t.Errorf("missing asset: %d", r.Code)
	}
	if r := serve(h, "POST", "/"); r.Code != 405 {
		t.Errorf("POST: %d", r.Code)
	}
	// Path tricks stay inside the build.
	if r := serve(h, "GET", "/../../etc/passwd"); r.Code == 200 && !strings.Contains(r.Body.String(), "app") {
		t.Errorf("traversal served something else: %d %q", r.Code, r.Body)
	}
}

// The cache contract is what makes an update reach the browser: the page and everything that is not named after its content is asked for again on
// every load, the content-hashed files are kept for good, and a missing file is never kept. A change to any of this changes what people see after
// a release, so each part is pinned.
func TestCacheContract(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":           {Data: []byte(`<html><script type="module" src="/assets/index-AAAA.js"></script></html>`)},
		"appearance-boot.js":   {Data: []byte("boot()")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		"assets/index-AAAA.js": {Data: []byte("app()")},
	})
	const forever = "public, max-age=31536000, immutable"
	for _, c := range []struct{ method, url, cache string }{
		{"GET", "/", "no-cache"},
		{"HEAD", "/", "no-cache"},
		{"GET", "/about", "no-cache"}, // a deep link is the page too
		{"GET", "/browse/3/42", "no-cache"},
		{"GET", "/?_=1761955200000", "no-cache"},   // the page's own question whether it is still current (web/src/lib/build.svelte.ts)
		{"GET", "/appearance-boot.js", "no-cache"}, // not named after its content, so it must be asked for again
		{"GET", "/favicon.svg", "no-cache"},
		{"GET", "/assets/index-AAAA.js", forever},
		{"GET", "/assets/index-GONE.js", "no-store"}, // a missing file is not kept by anything
		{"GET", "/gone.css", "no-store"},
	} {
		r := serve(h, c.method, c.url)
		if got := r.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s %s: Cache-Control = %q, want %q (status %d)", c.method, c.url, got, c.cache, r.Code)
		}
		if got := r.Header().Get("Cache-Control"); got == forever && !strings.HasPrefix(c.url, "/assets/") {
			t.Errorf("%s: a file that is not content-hashed must never be kept for good", c.url)
		}
	}
	// The question with a query string gets the same page as the plain address, so the answer is the current build whatever the query says.
	if a, b := serve(h, "GET", "/"), serve(h, "GET", "/?_=1"); a.Body.String() != b.Body.String() || !strings.Contains(b.Body.String(), "index-AAAA.js") {
		t.Errorf("the page with a query string differs from the page: %q vs %q", a.Body, b.Body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	r := serve(handler(fstest.MapFS{"index.html": {Data: []byte("x")}}), "GET", "/")
	csp := r.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(strings.Split(csp, "script-src")[1][:20], "unsafe") {
		t.Error("scripts must not allow inline code")
	}
	if r.Header().Get("X-Frame-Options") != "DENY" || r.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", r.Header())
	}
}

func TestExplainsAMissingBuild(t *testing.T) {
	r := serve(handler(fstest.MapFS{}), "GET", "/")
	if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "not built") {
		t.Errorf("%d %s", r.Code, r.Body)
	}
}
