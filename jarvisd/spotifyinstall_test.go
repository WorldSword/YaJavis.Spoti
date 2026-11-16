package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/newfpv/YaJavis/installer/soloist"
)

func TestPostSpotifyInstall(t *testing.T) {
	w := spotifyWeb(t, "", nil)
	inst := soloist.New(func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil }, "test")
	inst.Tmp = t.TempDir()
	inst.Get = func(ctx context.Context, url string) (*http.Response, error) {
		return nil, errors.New("нет сети")
	}
	w.engine.spotify.inst = inst
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		w.spotifyInstall(rec, httptest.NewRequest(http.MethodPost, "/api/spotify/install", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"api_key":""}`); rec.Code != 400 {
		t.Errorf("пустой ключ: код %d", rec.Code)
	}
	if rec := post(`{"api_key":"a$b"}`); rec.Code != 400 {
		t.Errorf("опасный ключ: код %d", rec.Code)
	}
	if rec := post(`{"api_key":"spak_abc"}`); rec.Code != 202 {
		t.Fatalf("корректный запрос: код %d: %s", rec.Code, rec.Body)
	}
	st := w.spotifyState()
	if st["installed"] != false || st["install"] == nil {
		t.Errorf("состояние без Soloist должно содержать install: %v", st)
	}
	for n := 0; n < 200 && inst.Status()["state"] == "running"; n++ {
		time.Sleep(10 * time.Millisecond)
	}
	if inst.Status()["state"] != "error" {
		t.Errorf("без сети установка должна закончиться ошибкой: %v", inst.Status())
	}
}

func TestPostSpotifyInstallAlreadyInstalled(t *testing.T) {
	w := spotifyWeb(t, fakeSoloistRoot(t), fakeSpotifyExec(&[]spotifyCall{}))
	rec := httptest.NewRecorder()
	w.spotifyInstall(rec, httptest.NewRequest(http.MethodPost, "/api/spotify/install", strings.NewReader(`{"api_key":"spak_abc"}`)))
	if rec.Code != 409 {
		t.Errorf("Soloist уже стоит: код %d, ждали 409", rec.Code)
	}
}
