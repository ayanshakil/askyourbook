package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	tests := []struct {
		path, contentType, contains string
		status                      int
	}{
		{"/", "text/html", `<script src="/static/app.js"`, 200},
		{"/static/app.js", "text/javascript", "readEvents", 200},
		{"/static/style.css", "text/css", "--accent", 200},
		{"/static/missing.js", "", "", 404},
		{"/nope", "", "", 404}, // "/" matches only the root
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.status {
			t.Errorf("%s: status %d, want %d", tt.path, rec.Code, tt.status)
			continue
		}
		if tt.status != 200 {
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.contentType) {
			t.Errorf("%s: Content-Type %q, want %s", tt.path, ct, tt.contentType)
		}
		if !strings.Contains(rec.Body.String(), tt.contains) {
			t.Errorf("%s: body lacks %q", tt.path, tt.contains)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP = %q", tt.path, csp)
		}
	}
}
