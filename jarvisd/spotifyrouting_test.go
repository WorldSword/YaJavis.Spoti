package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pausedSpotifyExec — Soloist отвечает «на паузе» (ctl-вызовы запоминаются).
func pausedSpotifyExec(calls *[]spotifyCall) spotifyExec {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "env" {
			return []byte(strings.Replace(spotifyNowFixture, `"status":"playing"`, `"status":"paused"`, 1)), nil
		}
		*calls = append(*calls, spotifyCall{name, args})
		return nil, nil
	}
}

func pausedSpotifyWeb(t *testing.T) (*Web, *[]spotifyCall) {
	t.Helper()
	var calls []spotifyCall
	w := spotifyWeb(t, fakeSoloistRoot(t), pausedSpotifyExec(&calls))
	if _, err := w.engine.spotify.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !w.engine.spotify.Active() || w.engine.spotify.Playing() {
		t.Fatal("фикстура: Spotify должен быть на паузе, но активен")
	}
	return w, &calls
}

// Spotify на паузе не должен перехватывать панель у играющей Алисы.
func TestPausedSpotifyDoesNotHideAlice(t *testing.T) {
	w, _ := pausedSpotifyWeb(t)
	w.engine.alice.mu.Lock()
	w.engine.alice.state = "playing"
	w.engine.alice.mu.Unlock()
	if got := w.musicNow(); got.Source == "spotify" {
		t.Errorf("Алиса играет, а панель показала Spotify на паузе: %+v", got)
	}
}

// Если больше ничего нет, Spotify на паузе показывается (можно нажать «продолжить»).
func TestPausedSpotifyShownWhenAlone(t *testing.T) {
	w, _ := pausedSpotifyWeb(t)
	if got := w.musicNow(); got.Source != "spotify" || !got.Paused {
		t.Errorf("ждали Spotify на паузе, получили %+v", got)
	}
}

// Кнопка панели при играющей Алисе не должна уходить в Soloist.
func TestPausedSpotifyDoesNotTakePanelPause(t *testing.T) {
	w, calls := pausedSpotifyWeb(t)
	w.engine.alice.mu.Lock()
	w.engine.alice.state = "playing"
	w.engine.alice.mu.Unlock()
	rec := httptest.NewRecorder()
	w.postMusic(rec, httptest.NewRequest(http.MethodPost, "/api/music", strings.NewReader(`{"action":"pause"}`)))
	if len(*calls) != 0 {
		t.Errorf("пауза панели ушла в Soloist при играющей Алисе: %+v", *calls)
	}
}

// «Продолжи» при паузе YouTube не должно будить Spotify.
func TestPausedSpotifyDoesNotTakeVoiceFromPausedYouTube(t *testing.T) {
	w, calls := pausedSpotifyWeb(t)
	w.engine.music.paused = true
	if ans, ok := w.engine.controlSpotify(context.Background(), "продолжи"); ok {
		t.Errorf("Spotify ответил %q при YouTube на паузе", ans)
	}
	if len(*calls) != 0 {
		t.Errorf("вызовы Soloist: %+v", *calls)
	}
}

// Перемотка с панели у Spotify уходит в Soloist: soloist ctl seek <мс>.
func TestSeekWithSpotifyCallsSoloist(t *testing.T) {
	var mu sync.Mutex
	var seen [][]string
	w := spotifyWeb(t, fakeSoloistRoot(t), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		seen = append(seen, append([]string{name}, args...))
		mu.Unlock()
		return []byte(spotifyNowFixture), nil
	})
	if _, err := w.engine.spotify.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	w.postMusic(rec, httptest.NewRequest(http.MethodPost, "/api/music", strings.NewReader(`{"action":"seek","sec":30}`)))
	if rec.Code != 200 {
		t.Fatalf("seek при Spotify: код %d: %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, c := range seen {
		if n := len(c); n >= 2 && c[n-2] == "seek" && c[n-1] == "30000" {
			found = true
		}
	}
	if !found {
		t.Errorf("ждали вызов ... ctl seek 30000, были: %v", seen)
	}
	if rec2 := httptest.NewRecorder(); true {
		w.postMusic(rec2, httptest.NewRequest(http.MethodPost, "/api/music", strings.NewReader(`{"action":"seek","sec":-5}`)))
		if rec2.Code != 400 {
			t.Errorf("отрицательная позиция: код %d, ждали 400", rec2.Code)
		}
	}
}

// Запуск Алисы или YouTube ставит играющий Spotify на паузу.
func TestAliceAndYouTubeStartPauseSpotify(t *testing.T) {
	for _, start := range []string{"alice", "youtube"} {
		paused := make(chan struct{}, 4)
		exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "env" {
				return []byte(spotifyNowFixture), nil
			}
			if len(args) > 0 && args[len(args)-1] == "pause" {
				paused <- struct{}{}
			}
			return nil, nil
		}
		sp := newSpotifyAt(fakeSoloistRoot(t), exec)
		if _, err := sp.Now(context.Background()); err != nil {
			t.Fatal(err)
		}
		e := &Engine{spotify: sp, music: &Music{}, alice: NewAliceMusic(nil)}
		if start == "alice" {
			e.aliceStarted()
		} else {
			e.youtubeStarted()
		}
		select {
		case <-paused:
		case <-time.After(2 * time.Second):
			t.Errorf("%s начал играть, Spotify не поставлен на паузу", start)
		}
	}
}

// Маска, к которой дописали новый ключ, — не ключ.
func TestPutSpotifyRejectsMaskPrefixedValue(t *testing.T) {
	root := fakeSoloistRoot(t)
	var calls []spotifyCall
	w := spotifyWeb(t, root, fakeSpotifyExec(&calls))
	rec := putSpotifyBody(t, w, `{"api_key":"задан, …abcdNEWKEY"}`)
	if rec.Code != 400 {
		t.Errorf("маска с припиской принята: код %d", rec.Code)
	}
}
