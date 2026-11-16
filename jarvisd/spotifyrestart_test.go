package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// lockedCalls — вызовы Soloist, безопасные для чтения из теста.
type lockedCalls struct {
	mu sync.Mutex
	c  []spotifyCall
}

func (l *lockedCalls) exec() spotifyExec {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.c = append(l.c, spotifyCall{name, args})
		return nil, nil
	}
}

func (l *lockedCalls) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, c := range l.c {
		out = append(out, c.name+" "+strings.Join(c.args, " "))
	}
	return strings.Join(out, "\n")
}

// yamSoloistRoot — корень в виде YAM: <yam>/share/horohi.soloist.
func yamSoloistRoot(t *testing.T) (root, yam string) {
	t.Helper()
	yam = t.TempDir()
	root = filepath.Join(yam, "share", "horohi.soloist")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "control.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, yam
}

func TestPutSpotifyRestartsSoloistYAM(t *testing.T) {
	root, yam := yamSoloistRoot(t)
	var l lockedCalls
	w := spotifyWeb(t, root, l.exec())
	rec := putSpotifyBody(t, w, `{"client_id":"cid","client_secret":"csecret12345"}`)
	if rec.Code != 200 {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	if got := l.joined(); !strings.Contains(got, filepath.Join(yam, "bin", "soloist")) || !strings.Contains(got, "restart") {
		t.Errorf("после сохранения ключей Soloist не перезапущен: %q", got)
	}
}

func TestPutSpotifyRestartsSoloistStandalone(t *testing.T) {
	root := fakeSoloistRoot(t)
	var l lockedCalls
	w := spotifyWeb(t, root, l.exec())
	if rec := putSpotifyBody(t, w, `{"device_name":"Кухня"}`); rec.Code != 200 {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	if got := l.joined(); !strings.Contains(got, filepath.Join(root, "bin", "soloist-cli")) || !strings.Contains(got, "restart") {
		t.Errorf("автономный Soloist не перезапущен: %q", got)
	}
}

func TestPutSpotifyNoRestartWhenNothingChanged(t *testing.T) {
	root := fakeSoloistRoot(t)
	var l lockedCalls
	w := spotifyWeb(t, root, l.exec())
	putSpotifyBody(t, w, `{"api_key":"k1234567890"}`)
	before := l.joined()
	putSpotifyBody(t, w, `{"api_key":"k1234567890"}`)             // то же значение
	putSpotifyBody(t, w, `{"api_key":"`+mask("k1234567890")+`"}`) // маска
	if l.joined() != before {
		t.Errorf("перезапуск без изменений: %q", l.joined())
	}
}

func TestPutSpotifyRestartFailureIsReportedNotFatal(t *testing.T) {
	root := fakeSoloistRoot(t)
	w := spotifyWeb(t, root, nil) // exec нет: перезапуск невозможен
	rec := putSpotifyBody(t, w, `{"api_key":"k1234567890"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "restart_error") {
		t.Errorf("настройки сохранены, но ошибка перезапуска должна быть в ответе: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(readFile(t, filepath.Join(root, "config.env")), "k1234567890") {
		t.Error("ключ не записан")
	}
}

func TestSpotifyRestartStandaloneWorks(t *testing.T) {
	root := fakeSoloistRoot(t)
	var l lockedCalls
	w := spotifyWeb(t, root, l.exec())
	rec := httptest.NewRecorder()
	w.spotifyRestart(rec, httptest.NewRequest(http.MethodPost, "/api/spotify/restart", nil))
	if rec.Code != 200 || !strings.Contains(l.joined(), "soloist-cli") {
		t.Errorf("перезапуск автономного Soloist: %d %q", rec.Code, l.joined())
	}
}
