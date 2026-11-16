package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type spotifyCall struct {
	name string
	args []string
}

func fakeSpotifyExec(calls *[]spotifyCall) spotifyExec {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, spotifyCall{name, args})
		return nil, nil
	}
}

func TestSpotifyRequest(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Джарвис, включи в Spotify Linkin Park Numb", "Linkin Park Numb", true},
		{"поставь в спотифае Кино Группа крови", "Кино Группа крови", true},
		{"включи песню Don't Stop Me Now в спотике", "Don't Stop Me Now", true},
		{"включи в Spotify", "", true},
		{"Джарвис, включи музыку", "", false},
		{"поставь на паузу в спотифай", "", false},
	}
	for _, c := range cases {
		got, ok := spotifyRequest(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%q → %q, %v; ждали %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSpotifyPlayArgv(t *testing.T) {
	var calls []spotifyCall
	root := fakeSoloistRoot(t)
	s := newSpotifyAt(root, fakeSpotifyExec(&calls))
	query := "Don't Stop; rm -rf /"
	if err := s.Play(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("вызовов %d, ждали 1", len(calls))
	}
	want := spotifyCall{"sh", []string{root + "/scripts/control.sh", "search", query}}
	if !reflect.DeepEqual(calls[0], want) {
		t.Errorf("вызов %+v, ждали %+v", calls[0], want)
	}
}

func TestSpotifyPlayEmpty(t *testing.T) {
	var calls []spotifyCall
	s := newSpotifyAt(fakeSoloistRoot(t), fakeSpotifyExec(&calls))
	if err := s.Play(context.Background(), "  "); err == nil {
		t.Error("пустой запрос должен вернуть ошибку")
	}
	if len(calls) != 0 {
		t.Errorf("exec вызван %d раз, ждали 0", len(calls))
	}
}

func TestSpotifyControlMapping(t *testing.T) {
	for action, arg := range map[string]string{"resume": "play", "pause": "pause", "next": "next", "prev": "prev", "toggle": "toggle"} {
		var calls []spotifyCall
		s := newSpotifyAt("/opt/soloist", fakeSpotifyExec(&calls))
		if err := s.Control(context.Background(), action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		want := spotifyCall{"sh", []string{"/opt/soloist/scripts/control.sh", arg}}
		if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
			t.Errorf("%s: вызовы %+v, ждали %+v", action, calls, want)
		}
	}
	var calls []spotifyCall
	s := newSpotifyAt(fakeSoloistRoot(t), fakeSpotifyExec(&calls))
	if err := s.Control(context.Background(), "explode"); err == nil {
		t.Error("неизвестное действие должно вернуть ошибку")
	}
	if len(calls) != 0 {
		t.Errorf("exec вызван %d раз, ждали 0", len(calls))
	}
}

func TestFindSoloistRoot(t *testing.T) {
	full, empty := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(full, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "scripts", "control.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findSoloistRoot(empty, full); got != full {
		t.Errorf("нашёл %q, ждали %q", got, full)
	}
	if got := findSoloistRoot(empty); got != "" {
		t.Errorf("в пустой папке нашёл %q", got)
	}
	if s := newSpotifyAt("", nil); s.Installed() {
		t.Error("без root Soloist не может считаться установленным")
	}
	if s := newSpotifyAt(full, nil); !s.Installed() || s.Root() != full {
		t.Error("с root Soloist должен считаться установленным")
	}
}

func TestSpotifyOnPlay(t *testing.T) {
	ctx := context.Background()
	var calls []spotifyCall
	s := newSpotifyAt(fakeSoloistRoot(t), fakeSpotifyExec(&calls))
	n := 0
	s.OnPlay = func() { n++ }
	if err := s.Play(ctx, "Кино"); err != nil {
		t.Fatal(err)
	}
	if err := s.Control(ctx, "resume"); err != nil {
		t.Fatal(err)
	}
	if err := s.Control(ctx, "pause"); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("OnPlay вызван %d раз, ждали 2 (запуск и продолжение, но не пауза)", n)
	}
	failing := newSpotifyAt("/opt/soloist", func(context.Context, string, ...string) ([]byte, error) {
		return nil, os.ErrNotExist
	})
	failing.OnPlay = func() { n++ }
	if err := failing.Play(ctx, "Кино"); err == nil {
		t.Error("ошибка запуска должна вернуться")
	}
	if n != 2 {
		t.Errorf("OnPlay вызван после неудачного запуска: n=%d", n)
	}
}
