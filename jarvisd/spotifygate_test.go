package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// gateWorld — Soloist с состоянием: «pause»/«play» меняют, играет он или нет.
type gateWorld struct {
	mu      sync.Mutex
	playing bool
	cmds    []string
}

func (g *gateWorld) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if name == "env" {
		out := spotifyNowFixture
		if !g.playing {
			out = strings.Replace(out, `"status":"playing"`, `"status":"paused"`, 1)
		}
		return []byte(out), nil
	}
	if len(args) >= 2 {
		c := args[1]
		g.cmds = append(g.cmds, c)
		switch c {
		case "pause":
			g.playing = false
		case "play", "next":
			g.playing = true
		}
	}
	return nil, nil
}

func (g *gateWorld) log() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.cmds...)
}

func gateEngine(t *testing.T, playing bool) (*Engine, *gateWorld) {
	t.Helper()
	w := &gateWorld{playing: playing}
	sp := newSpotifyAt(fakeSoloistRoot(t), w.exec)
	e := &Engine{music: &Music{}, alice: NewAliceMusic(nil), spotify: sp}
	e.vg.delay = 30 * time.Millisecond
	if _, err := sp.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e, w
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for n := 0; n < 200; n++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

// Soloist держит звуковое устройство: пока говорит Джарвис, музыку ставим на паузу и потом возвращаем.
func TestVoiceGatePausesAndResumes(t *testing.T) {
	e, w := gateEngine(t, true)
	e.spotifyVoiceStart()
	if got := w.log(); len(got) != 1 || got[0] != "pause" {
		t.Fatalf("к началу голоса Spotify должен быть на паузе, команды: %v", got)
	}
	e.spotifyVoiceEnd()
	waitFor(t, "возобновление", func() bool { return len(w.log()) == 2 })
	if got := w.log(); got[1] != "play" {
		t.Errorf("команды: %v, ждали pause, play", got)
	}
}

func TestVoiceGateIgnoresIdleSpotify(t *testing.T) {
	e, w := gateEngine(t, false)
	e.spotifyVoiceStart()
	e.spotifyVoiceEnd()
	time.Sleep(120 * time.Millisecond)
	if got := w.log(); len(got) != 0 {
		t.Errorf("Spotify не играл, а команды были: %v", got)
	}
}

// «Джарвис, пауза» посреди разговора: после ответа музыку не возвращаем.
func TestVoiceGateRespectsUserCommand(t *testing.T) {
	e, w := gateEngine(t, true)
	e.spotifyVoiceStart()
	if err := e.spotify.Control(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.playing = false // человек сказал «пауза»
	w.mu.Unlock()
	_, _ = e.spotify.Now(context.Background())
	e.spotifyVoiceEnd()
	time.Sleep(150 * time.Millisecond)
	if got := w.log(); len(got) != 2 {
		t.Errorf("после команды человека музыка не должна возвращаться, команды: %v", got)
	}
}

// Две реплики подряд: музыка возвращается один раз, после последней.
func TestVoiceGateWaitsForLastAnswer(t *testing.T) {
	e, w := gateEngine(t, true)
	e.spotifyVoiceStart()
	e.spotifyVoiceEnd()
	e.spotifyVoiceStart()
	time.Sleep(80 * time.Millisecond)
	if got := w.log(); len(got) != 1 {
		t.Fatalf("между репликами музыка не должна играть: %v", got)
	}
	e.spotifyVoiceEnd()
	waitFor(t, "возобновление", func() bool { return len(w.log()) == 2 })
}

func TestVoiceGateWithoutSpotify(t *testing.T) {
	e := &Engine{music: &Music{}, alice: NewAliceMusic(nil)}
	e.spotifyVoiceStart()
	e.spotifyVoiceEnd()
}
