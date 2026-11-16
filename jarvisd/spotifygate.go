package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// Soloist занимает звуковое устройство сам, и голос Джарвиса при играющем Spotify не слышен.
// Поэтому на время голоса Джарвис ставит Spotify на паузу и потом возвращает его (как делает Алиса,
// только возвращает): но только если никто не вмешался — человек не сказал «пауза», не включилось другое.
type voiceGate struct {
	mu    sync.Mutex
	n     int    // сколько ответов Джарвиса сейчас играет
	armed bool   // мы поставили Spotify на паузу и должны вернуть
	seq   uint64 // счётчик команд Soloist сразу после нашей паузы
	timer *time.Timer
	delay time.Duration // пауза после последнего ответа до возврата музыки; 0 — по умолчанию
}

func (g *voiceGate) resumeDelay() time.Duration {
	if g.delay > 0 {
		return g.delay
	}
	return 4 * time.Second
}

// spotifyVoiceStart — вызывается перед тем, как Джарвис начнёт говорить. Возвращается, когда пауза включена.
func (e *Engine) spotifyVoiceStart() {
	sp := e.spotify
	if sp == nil {
		return
	}
	g := &e.vg
	g.mu.Lock()
	g.n++
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	need := !g.armed && sp.Playing()
	g.mu.Unlock()
	if !need {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sp.Control(ctx, "pause"); err != nil {
		log.Printf("spotify: пауза на время голоса Джарвиса: %v", err)
		return
	}
	_, _ = sp.Now(ctx)
	g.mu.Lock()
	g.armed, g.seq = true, sp.CmdSeq()
	g.mu.Unlock()
}

// spotifyVoiceEnd — ответ Джарвиса закончился.
func (e *Engine) spotifyVoiceEnd() {
	if e.spotify == nil {
		return
	}
	g := &e.vg
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n > 0 {
		g.n--
	}
	if g.n == 0 && g.armed {
		if g.timer != nil {
			g.timer.Stop()
		}
		g.timer = time.AfterFunc(g.resumeDelay(), e.spotifyResumeAfterVoice)
	}
}

func (e *Engine) spotifyResumeAfterVoice() {
	sp := e.spotify
	g := &e.vg
	g.mu.Lock()
	if g.n > 0 || !g.armed {
		g.mu.Unlock()
		return
	}
	g.armed, g.timer = false, nil
	seq := g.seq
	g.mu.Unlock()
	if sp.CmdSeq() != seq || !sp.Paused() || e.music.Active() || e.alice.Active() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sp.Control(ctx, "resume"); err != nil {
		log.Printf("spotify: возврат музыки после голоса Джарвиса: %v", err)
	}
}
