package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSoloistRoot — папка, которую findSoloistRoot примет за установленный Soloist (автономная: конфиги рядом).
func fakeSoloistRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "control.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spotify_api.conf"), []byte("SPOTIFY_CLIENT_ID=\"testid\"\nSPOTIFY_CLIENT_SECRET=\"testsecret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func spotifyWeb(t *testing.T, root string, exec spotifyExec) *Web {
	t.Helper()
	sp := newSpotifyAt(root, exec)
	return &Web{engine: &Engine{music: &Music{}, alice: NewAliceMusic(nil), spotify: sp}}
}

func playingExec(calls *[]spotifyCall) spotifyExec {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "env" {
			return []byte(spotifyNowFixture), nil
		}
		*calls = append(*calls, spotifyCall{name, args})
		return nil, nil
	}
}

func putSpotifyBody(t *testing.T, w *Web, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	w.putSpotify(rec, httptest.NewRequest(http.MethodPut, "/api/spotify", strings.NewReader(body)))
	return rec
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestSoloistConfDir(t *testing.T) {
	if got := soloistConfDir("/data/yam/share/horohi.soloist"); got != "/data/yam/etc/horohi.soloist" {
		t.Errorf("YAM: %q", got)
	}
	if got := soloistConfDir("/data/opt/soloist"); got != "/data/opt/soloist" {
		t.Errorf("автономная: %q", got)
	}
}

func TestSpotifyStateMasksKeys(t *testing.T) {
	root := fakeSoloistRoot(t)
	secret := "abcdef123456"
	if err := os.WriteFile(filepath.Join(root, "config.env"), []byte("API_KEY=\""+secret+"\"\nDEVICE_NAME=\"Кухня\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := spotifyWeb(t, root, nil)
	st := w.spotifyState()
	if st["installed"] != true || st["api_key"] != mask(secret) || st["device_name"] != "Кухня" {
		t.Errorf("состояние %+v", st)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), secret) {
		t.Errorf("ключ попал в ответ панели открытым текстом: %s", raw)
	}
}

func TestPutSpotifyWritesConfig(t *testing.T) {
	root := fakeSoloistRoot(t)
	w := spotifyWeb(t, root, nil)
	rec := putSpotifyBody(t, w, `{"api_key":"k1234567890","device_name":"Кухня","client_id":"cid","client_secret":"csecret12345"}`)
	if rec.Code != 200 {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	env := readFile(t, filepath.Join(root, "config.env"))
	if !strings.Contains(env, `API_KEY="k1234567890"`) || !strings.Contains(env, `DEVICE_NAME="Кухня"`) {
		t.Errorf("config.env: %q", env)
	}
	conf := readFile(t, filepath.Join(root, "spotify_api.conf"))
	if !strings.Contains(conf, `SPOTIFY_CLIENT_ID="cid"`) || !strings.Contains(conf, `SPOTIFY_CLIENT_SECRET="csecret12345"`) {
		t.Errorf("spotify_api.conf: %q", conf)
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{"config.env", "spotify_api.conf"} {
			if st, err := os.Stat(filepath.Join(root, f)); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("%s: права %v, ждали 0600", f, st.Mode().Perm())
			}
		}
	}
}

func TestPutSpotifyKeepsOtherLines(t *testing.T) {
	root := fakeSoloistRoot(t)
	old := "# комментарий владельца\nAPI_KEY=\"old\"\nEXTRA=\"1\"\n"
	if err := os.WriteFile(filepath.Join(root, "config.env"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	putSpotifyBody(t, spotifyWeb(t, root, nil), `{"device_name":"Зал"}`)
	got := readFile(t, filepath.Join(root, "config.env"))
	if !strings.Contains(got, "# комментарий владельца") || !strings.Contains(got, `EXTRA="1"`) || !strings.Contains(got, `API_KEY="old"`) || !strings.Contains(got, `DEVICE_NAME="Зал"`) {
		t.Errorf("прочие строки config.env потерялись: %q", got)
	}
}

func TestPutSpotifyMaskedKeepsKey(t *testing.T) {
	root := fakeSoloistRoot(t)
	old := "API_KEY=\"realsecretkey1\"\n"
	p := filepath.Join(root, "config.env")
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := putSpotifyBody(t, spotifyWeb(t, root, nil), `{"api_key":"`+mask("realsecretkey1")+`"}`)
	if rec.Code != 200 {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	if got := readFile(t, p); got != old {
		t.Errorf("маска затёрла ключ: %q", got)
	}
}

func TestPutSpotifyRejectsShellChars(t *testing.T) {
	for _, v := range []string{`a"b`, `$(reboot)`, "`x`", "a\nb", `a\b`, "a\rb", strings.Repeat("x", 201)} {
		root := fakeSoloistRoot(t)
		p := filepath.Join(root, "config.env")
		old := "API_KEY=\"old\"\n"
		if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"device_name": "Ок", "api_key": v})
		rec := putSpotifyBody(t, spotifyWeb(t, root, nil), string(body))
		if rec.Code != 400 {
			t.Errorf("%q: код %d, ждали 400", v, rec.Code)
		}
		if got := readFile(t, p); got != old {
			t.Errorf("%q: файл изменён: %q", v, got)
		}
		if got := readFile(t, filepath.Join(root, "spotify_api.conf")); !strings.Contains(got, `"testid"`) || strings.Contains(got, "Ок") {
			t.Errorf("%q: spotify_api.conf изменён: %q", v, got)
		}
	}
}

func TestSpotifyStateNotInstalled(t *testing.T) {
	for _, w := range []*Web{spotifyWeb(t, "", nil), {engine: &Engine{music: &Music{}, alice: NewAliceMusic(nil)}}} {
		if st := w.spotifyState(); st["installed"] != false {
			t.Errorf("без Soloist installed=%v", st["installed"])
		}
		if rec := putSpotifyBody(t, w, `{"api_key":"x"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "Soloist не найден") {
			t.Errorf("PUT без Soloist: %d %s", rec.Code, rec.Body)
		}
		rec := httptest.NewRecorder()
		w.spotifyRestart(rec, httptest.NewRequest(http.MethodPost, "/api/spotify/restart", nil))
		if rec.Code != 400 {
			t.Errorf("перезапуск без Soloist: код %d", rec.Code)
		}
		if w.musicNow().Source == "" {
			t.Error("musicNow без Spotify должен работать как раньше")
		}
	}
}

func TestMusicNowPrefersSpotify(t *testing.T) {
	var calls []spotifyCall
	w := spotifyWeb(t, fakeSoloistRoot(t), playingExec(&calls))
	if _, err := w.engine.spotify.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.musicNow(); got.Source != "spotify" || got.Track.Title != "Numb" {
		t.Errorf("Spotify играет, а панель показала %+v", got)
	}
	_, cancel := context.WithCancel(context.Background())
	w.engine.music.cancel = cancel
	if got := w.musicNow(); got.Source != "youtube" {
		t.Errorf("YouTube играет, источник %q", got.Source)
	}
}

func TestPostMusicRoutesToSpotify(t *testing.T) {
	for action, want := range map[string]string{"pause": "pause", "resume": "play", "next": "next", "prev": "prev", "stop": "pause"} {
		var calls []spotifyCall
		w := spotifyWeb(t, fakeSoloistRoot(t), playingExec(&calls))
		if _, err := w.engine.spotify.Now(context.Background()); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		w.postMusic(rec, httptest.NewRequest(http.MethodPost, "/api/music", strings.NewReader(`{"action":"`+action+`"}`)))
		if rec.Code != 200 {
			t.Fatalf("%s: код %d: %s", action, rec.Code, rec.Body)
		}
		if len(calls) != 1 || calls[0].args[len(calls[0].args)-1] != want {
			t.Errorf("%s: вызовы Soloist %+v, ждали последний аргумент %q", action, calls, want)
		}
	}
}

func TestSpotifyStateRunning(t *testing.T) {
	var calls []spotifyCall
	w := spotifyWeb(t, fakeSoloistRoot(t), playingExec(&calls))
	if st := w.spotifyState(); st["running"] != false || st["playing"] != false {
		t.Errorf("до первого опроса: %+v", st)
	}
	if _, err := w.engine.spotify.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := w.spotifyState(); st["running"] != true || st["playing"] != true {
		t.Errorf("после опроса: %+v", st)
	}
}

func TestSpotifyNilSafe(t *testing.T) {
	var s *Spotify
	if s.Installed() || s.Active() || s.Playing() || s.Paused() || s.Running() || s.Snapshot().Source != "spotify" {
		t.Error("nil *Spotify должен вести себя как «не установлен»")
	}
}
