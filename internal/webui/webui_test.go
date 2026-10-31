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
