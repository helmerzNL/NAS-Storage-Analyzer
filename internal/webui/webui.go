// Package webui serves the single-page web interface built by Vite and
// embedded in the binary, so one container holds the API and the UI.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const notBuilt = `<!doctype html><meta charset="utf-8"><title>NAS Storage Analyzer</title>
<body style="font-family:system-ui;max-width:40em;margin:3em auto;padding:0 1em">
<h1>The web interface is not built</h1>
<p>This binary was compiled without the interface. Build it with <code>npm --prefix web ci &amp;&amp; npm --prefix web run build</code>
and compile again, or use the Docker image, which does both.</p><p>The API is available under <code>/api/v1</code>.</p></body>`

// csp restricts the page to its own scripts and styles. Inline styles are
// allowed because Svelte sets some through the DOM, scripts never are.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; " +
	"font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Handler serves the interface: files from the build, and index.html for any
// other path so that deep links to client-side routes work after a reload.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return handler(sub)
}

func handler(files fs.FS) http.Handler {
	_, err := fs.Stat(files, "index.html")
	built := err == nil
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(notBuilt))
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if fi, err := fs.Stat(files, name); err != nil || fi.IsDir() {
			if strings.Contains(path.Base(name), ".") && !strings.HasSuffix(name, ".html") {
				// A missing asset must not turn into the HTML page. A 404 may be kept by a cache without any header saying so, and the name that
				// is missing now may be the one the next build brings (a proxy asked during an update): that answer must not outlive the update.
				h.Set("Cache-Control", "no-store")
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}
		if strings.HasPrefix(name, "assets/") { // content-hashed file names
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, files, name)
	})
}
