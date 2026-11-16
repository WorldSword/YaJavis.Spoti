package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Обложки Spotify лежат на i.scdn.co: без него в CSP панель их не рисует.
func TestCSPAllowsSpotifyCovers(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	w := &Web{store: st}
	rec := httptest.NewRecorder()
	w.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src") || !strings.Contains(csp, "https://i.scdn.co") {
		t.Errorf("CSP не пускает обложки Spotify: %q", csp)
	}
}
