// Package web serves the browser chat UI: a static page that talks to the
// JSON API on the same origin. The files are embedded in the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var files embed.FS

// csp allows only same-origin scripts, styles and API calls; the page uses
// no inline scripts or styles.
const csp = "default-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Register adds the UI routes: the page at "/" and its assets under /static/.
func Register(mux *http.ServeMux) {
	static, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	assets := http.StripPrefix("/static/", http.FileServerFS(static))

	mux.Handle("GET /{$}", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})))
	mux.Handle("GET /static/", secure(assets))
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
