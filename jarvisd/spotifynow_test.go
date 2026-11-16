package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Форма JSON — предположение по README Soloist (item.uri и context.uri подтверждены, остальные поля нет):
// сверить с реальным выводом `soloist ctl -w 127.0.0.1:9090 now --json` на колонке.
const spotifyNowFixture = `{"is_playing":true,"progress_ms":42000,
 "item":{"uri":"spotify:track:abc","name":"Numb","duration_ms":185000,
  "artists":[{"name":"Linkin Park"},{"name":"Jay-Z"}],
  "album":{"images":[{"url":"https://i.scdn.co/image/x"}]}},
 "context":{"uri":"spotify:playlist:p"}}`

func TestParseSpotifyNow(t *testing.T) {
	got, err := parseSpotifyNow([]byte(spotifyNowFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := SpotifyNow{Playing: true, Title: "Numb", Artist: "Linkin Park, Jay-Z", Cover: "https://i.scdn.co/image/x",
		URI: "spotify:track:abc", Pos: 42, Dur: 185}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("разобрано %+v, ждали %+v", got, want)
	}
}

func TestParseSpotifyNowTolerantOfLogNoise(t *testing.T) {
	got, err := parseSpotifyNow([]byte("level=warn something\n" + spotifyNowFixture + "\n"))
	if err != nil || got.Title != "Numb" {
		t.Errorf("шум вокруг JSON сломал разбор: %+v, %v", got, err)
	}
}

func TestParseSpotifyNowDegraded(t *testing.T) {
	for _, in := range []string{"", "not json", "{}", `{"item":null}`} {
		got, err := parseSpotifyNow([]byte(in))
		if err == nil && got.URI != "" {
			t.Errorf("%q: появился трек %+v из пустоты", in, got)
		}
	}
	got, err := parseSpotifyNow([]byte(`{"is_playing":true,"item":{"uri":"spotify:episode:abc"}}`))
	if err != nil || got.Title != "Spotify" || !got.Playing {
		t.Errorf("подкаст без названия: %+v, %v; ждали Title «Spotify» и Playing", got, err)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func spotifyWithNow(out []byte, err error, clk *fakeClock) *Spotify {
	s := newSpotifyAt("/opt/soloist", func(context.Context, string, ...string) ([]byte, error) { return out, err })
	s.clock = clk.now
	return s
}

func TestSpotifyActiveExpires(t *testing.T) {
	clk := &fakeClock{time.Unix(1000, 0)}
	s := spotifyWithNow([]byte(spotifyNowFixture), nil, clk)
	if s.Active() {
		t.Fatal("до первого опроса Spotify не активен")
	}
	if _, err := s.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Active() || !s.Playing() || s.Paused() {
		t.Fatal("после опроса с играющим треком: Active и Playing, не Paused")
	}
	clk.t = clk.t.Add(4 * time.Second)
	if s.Active() {
		t.Error("через 4 с без обновления Spotify не должен считаться активным")
	}
	clk.t = time.Unix(1000, 0)
	s.exec = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("демон не отвечает")
	}
	if _, err := s.Now(context.Background()); err == nil {
		t.Fatal("ошибка запуска должна вернуться")
	}
	if s.Active() {
		t.Error("после ошибки опроса Spotify не активен")
	}
}

func TestSpotifySnapshot(t *testing.T) {
	clk := &fakeClock{time.Unix(1000, 0)}
	paused := `{"is_playing":false,"progress_ms":5000,"item":{"uri":"spotify:track:abc","name":"Numb","duration_ms":185000,"artists":[{"name":"Linkin Park"}]}}`
	s := spotifyWithNow([]byte(paused), nil, clk)
	if _, err := s.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if snap.Source != "spotify" || !snap.Paused || snap.Playing || snap.Track.Title != "Numb" || snap.Track.Author != "Linkin Park" || snap.Dur != 185 || snap.Pos != 5 {
		t.Errorf("снимок %+v", snap)
	}
}

func TestSpotifyNowArgv(t *testing.T) {
	var calls []spotifyCall
	clk := &fakeClock{time.Unix(1000, 0)}
	s := newSpotifyAt("/opt/soloist", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, spotifyCall{name, args})
		return []byte(spotifyNowFixture), nil
	})
	s.clock = clk.now
	if _, err := s.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := spotifyCall{"env", []string{"LD_LIBRARY_PATH=/opt/soloist/lib64", "/opt/soloist/bin/soloist", "ctl", "-w", "127.0.0.1:9090", "now", "--json"}}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Errorf("вызовы %+v, ждали %+v", calls, want)
	}
}

func TestSpotifyNowFallsBackToLoader(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "lib64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib64", "ld-linux-aarch64.so.1"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []spotifyCall
	s := newSpotifyAt(root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, spotifyCall{name, args})
		if len(calls) == 1 {
			return nil, errors.New("exec format error")
		}
		return []byte(spotifyNowFixture), nil
	})
	got, err := s.Now(context.Background())
	if err != nil || got.Title != "Numb" {
		t.Fatalf("запасной запуск через загрузчик не сработал: %+v, %v", got, err)
	}
	if len(calls) != 2 || calls[1].args[1] != root+"/lib64/ld-linux-aarch64.so.1" {
		t.Errorf("второй вызов должен идти через ld-linux: %+v", calls)
	}
}

func TestSpotifyRunPolls(t *testing.T) {
	s := newSpotifyAt("/opt/soloist", func(context.Context, string, ...string) ([]byte, error) {
		return []byte(spotifyNowFixture), nil
	})
	s.poll = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !s.Active() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !s.Active() {
		t.Error("фоновый опрос не обновил состояние")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run не завершился после отмены контекста")
	}
}
