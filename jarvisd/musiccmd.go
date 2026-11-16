package main

import (
	"context"
	"log"
	"time"
)

// controlMusic проверяется до передачи команды Алисе. На паузе управление остаётся у YouTube.
func (e *Engine) controlMusic(text string) (string, bool) {
	return e.controlMusicContext(context.Background(), text)
}

func (e *Engine) controlMusicContext(parent context.Context, text string) (string, bool) {
	if !e.music.Playing() { // играет YouTube — команды ему; иначе Spotify, если он активен
		if answer, ok := e.controlSpotify(parent, text); ok {
			return answer, true
		}
	}
	if answer, ok := e.resumeSavedRequest(parent, text); ok {
		return answer, true
	}
	if answer, ok := e.music.respondProposal(parent, text); ok {
		return answer, true
	}
	if !e.music.Active() {
		return "", false
	}
	action, secs, abs, ok := musicCommand(text)
	if !ok {
		return "", false
	}
	var err error
	answer := ""
	switch action {
	case "what":
		answer = e.music.Status()
	// ответы короткие: их говорят вслух поверх приглушённой музыки
	case "pause":
		e.music.Pause()
		answer = "Пауза."
	case "resume":
		err = e.music.Resume()
		answer = "Продолжаю."
	case "seek":
		err = e.music.Seek(secs, abs)
		answer = "Перемотал на " + clock(e.music.Pos()) + "."
	case "stop":
		e.music.Stop()
		answer = "Выключил."
	case "radio":
		_ = e.store.Update(func(s *Settings) error { s.RadioOff = false; return nil })
		e.music.mu.Lock()
		e.music.queue, e.music.results, e.music.mix = nil, nil, nil
		e.music.personal = false
		e.music.mu.Unlock()
		ctx, cancel := context.WithTimeout(parent, 45*time.Second)
		defer cancel()
		var t Track
		t, err = e.music.Next(ctx)
		answer = "Похожее: " + t.Title + "."
	case "next":
		e.music.mu.Lock()
		seed := e.music.cur.ID
		e.music.mu.Unlock()
		if seed != "" {
			return e.music.proposeRelated(parent), true
		}
		ctx, cancel := context.WithTimeout(parent, 45*time.Second)
		defer cancel()
		var t Track
		t, err = e.music.Next(ctx)
		answer = "Включаю следующее: " + t.Title + "."
	}
	if err != nil {
		answer = "Не получилось: " + shortErr(err)
	}
	log.Printf("музыка: «%s» → %s", text, answer)
	return answer, true
}

func (e *Engine) sayMusicStatus(answer string) {
	e.sayMusicStatusContext(context.Background(), answer)
}

func (e *Engine) sayMusicStatusContext(ctx context.Context, answer string) {
	if e.notice != nil {
		e.notice(ctx, answer)
		return
	}
	if e.proxy == nil {
		return
	}
	s := e.store.Get()
	p := s.ActiveProfile()
	voicePrompt := ""
	for _, v := range s.LiveVoices {
		if v.ID == p.Voice {
			voicePrompt = v.Prompt
		}
	}
	if _, err := e.sayAloudContext(ctx, s, p, voicePrompt, answer, 25*time.Second); err != nil {
		log.Printf("музыка: озвучка статуса: %v", err)
	}
}

func (e *Engine) beginMedia() (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	e.mu.Lock()
	e.opCancel = cancel
	e.mu.Unlock()
	return ctx, func() { cancel(); e.mu.Lock(); e.opCancel = nil; e.mu.Unlock() }
}
