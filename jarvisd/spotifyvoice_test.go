package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func definedTools(t *Tools) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, d := range t.Definitions() {
		if m, ok := d.(map[string]any); ok {
			out[m["name"].(string)] = m
		}
	}
	return out
}

func TestPlaySpotifyToolDefined(t *testing.T) {
	tools := &Tools{token: func() string { return "" }}
	if _, ok := definedTools(tools)["play_spotify"]; ok {
		t.Error("без Soloist инструмент play_spotify объявляться не должен")
	}
	tools.SpotifyPlay = func(context.Context, string) string { return "" }
	def, ok := definedTools(tools)["play_spotify"]
	if !ok {
		t.Fatal("с Soloist инструмент play_spotify должен быть объявлен")
	}
	req := def["parameters"].(map[string]any)["required"].([]any)
	if len(req) != 1 || req[0] != "query" {
		t.Errorf("обязательные параметры %v, ждали [query]", req)
	}
}

func TestPlaySpotifyCall(t *testing.T) {
	got := ""
	tools := &Tools{SpotifyPlay: func(_ context.Context, q string) string { got = q; return "ответ" }}
	if r := tools.Call(context.Background(), "play_spotify", `{"query":"Кино Группа крови"}`); r != "ответ" || got != "Кино Группа крови" {
		t.Errorf("результат %q, запрос %q", r, got)
	}
	got = ""
	for _, args := range []string{`{"query":"  "}`, `не json`} {
		if r := tools.Call(context.Background(), "play_spotify", args); r != "Не понял, что включить в Spotify." || got != "" {
			t.Errorf("%q → %q, SpotifyPlay вызван с %q", args, r, got)
		}
	}
}

func primedSpotifyEngine(t *testing.T) (*Engine, *[]spotifyCall) {
	t.Helper()
	var calls []spotifyCall
	sp := newSpotifyAt(fakeSoloistRoot(t), playingExec(&calls))
	if _, err := sp.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Engine{spotify: sp, music: &Music{}, alice: NewAliceMusic(nil)}, &calls
}

func TestControlSpotify(t *testing.T) {
	cases := []struct{ text, answer, arg string }{
		{"пауза", "Пауза.", "pause"},
		{"следующее", "Включаю следующее.", "next"},
		{"продолжи", "Продолжаю.", "play"},
		{"выключи музыку", "Выключил.", "pause"},
	}
	for _, c := range cases {
		e, calls := primedSpotifyEngine(t)
		got, ok := e.controlSpotify(context.Background(), c.text)
		if !ok || got != c.answer {
			t.Errorf("%q → %q, %v; ждали %q", c.text, got, ok, c.answer)
		}
		if len(*calls) != 1 || (*calls)[0].args[len((*calls)[0].args)-1] != c.arg {
			t.Errorf("%q: вызовы Soloist %+v, ждали аргумент %q", c.text, *calls, c.arg)
		}
	}
	e, calls := primedSpotifyEngine(t)
	got, ok := e.controlSpotify(context.Background(), "что играет")
	if !ok || !strings.Contains(got, "Numb") || !strings.Contains(got, "Linkin Park") || len(*calls) != 0 {
		t.Errorf("«что играет» → %q, %v, вызовы %+v", got, ok, *calls)
	}
}

func TestControlSpotifyInactive(t *testing.T) {
	var calls []spotifyCall
	e := &Engine{spotify: newSpotifyAt(fakeSoloistRoot(t), playingExec(&calls)), music: &Music{}, alice: NewAliceMusic(nil)}
	if got, ok := e.controlSpotify(context.Background(), "пауза"); ok || got != "" || len(calls) != 0 {
		t.Errorf("при неактивном Spotify: %q, %v, вызовы %+v", got, ok, calls)
	}
	var none *Engine = &Engine{}
	if _, ok := none.controlSpotify(context.Background(), "пауза"); ok {
		t.Error("без Spotify команда не наша")
	}
}

func TestPlaySpotifyContext(t *testing.T) {
	var calls []spotifyCall
	e := &Engine{spotify: newSpotifyAt(fakeSoloistRoot(t), playingExec(&calls))}
	got := e.playSpotifyContext(context.Background(), "Кино Группа крови")
	if got != "Включаю в Spotify: Кино Группа крови." || len(calls) != 1 || calls[0].args[len(calls[0].args)-2] != "search" {
		t.Errorf("успех: %q, вызовы %+v", got, calls)
	}
	calls = nil
	if got := e.playSpotifyContext(context.Background(), " "); got != "Что включить в Spotify?" || len(calls) != 0 {
		t.Errorf("пустой запрос: %q, вызовы %+v", got, calls)
	}
	bad := &Engine{spotify: newSpotifyAt(fakeSoloistRoot(t), func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("демон не отвечает")
	})}
	if got := bad.playSpotifyContext(context.Background(), "Кино"); !strings.HasPrefix(got, "Не получилось") {
		t.Errorf("ошибка запуска: %q", got)
	}
}

func TestSpotifyStartedStopsYouTube(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	m := &Music{cancel: cancel, st: &ytStream{}}
	var said []string
	old := aliceTextFunc
	aliceTextFunc = func(text string, silent bool) error { said = append(said, text); return nil }
	defer func() { aliceTextFunc = old }()
	e := &Engine{music: m, alice: NewAliceMusic(nil)}
	e.spotifyStarted()
	if m.Playing() {
		t.Error("YouTube должен остановиться, когда зазвучал Spotify")
	}
	if len(said) != 0 {
		t.Errorf("Алисе ничего не играло, а ей сказали %v", said)
	}
	e.alice.state = "playing"
	e.spotifyStarted()
	if len(said) != 1 || said[0] != "пауза" {
		t.Errorf("у Алисы играла музыка, ждали «пауза», получили %v", said)
	}
}

func TestSpotifyPlayStopsYouTubeViaHook(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	m := &Music{cancel: cancel, st: &ytStream{}}
	var calls []spotifyCall
	e := &Engine{music: m, alice: NewAliceMusic(nil), spotify: newSpotifyAt(fakeSoloistRoot(t), playingExec(&calls))}
	e.spotify.OnPlay = e.spotifyStarted
	if err := e.spotify.Play(context.Background(), "Кино"); err != nil {
		t.Fatal(err)
	}
	if m.Playing() {
		t.Error("запуск Spotify через OnPlay должен остановить YouTube")
	}
}
