package main

// Страница «Музыка» в настройках: что играет, управление, история.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// MusicState — снимок для страницы «Музыка».
type MusicState struct {
	Source  string  `json:"source"` // youtube | yandex
	Cover   string  `json:"cover,omitempty"`
	Link    string  `json:"link,omitempty"`
	Album   string  `json:"album,omitempty"`
	Playing bool    `json:"playing"`
	Paused  bool    `json:"paused"`
	Loading bool    `json:"loading"`
	Track   Track   `json:"track"`
	Pos     float64 `json:"pos"`
	Dur     float64 `json:"dur"`
	Client  string  `json:"client"`
	Next    []Track `json:"next"`
	Queue   int     `json:"queue"` // сколько из Next — очередь (дальше — похожее)
	Radio   bool    `json:"radio"`
	History []Track `json:"history"`
}

func (m *Music) Snapshot() MusicState {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := MusicState{Source: "youtube", Playing: m.cancel != nil, Paused: m.paused, Loading: m.loadingCancel != nil, Track: m.cur, Pos: m.pos,
		Next: m.upNext(20), Queue: len(m.queue), Radio: m.radioOn(), History: append([]Track{}, m.history...)}
	if m.st != nil {
		s.Dur, s.Client = m.st.dur, m.st.client
	}
	if !s.Playing && !s.Paused {
		s.Track, s.Pos, s.Dur = Track{}, 0, 0
	}
	return s
}

// remember — в историю (последние 30, без повторов).
func (m *Music) remember(t Track) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := []Track{t}
	for _, x := range m.history {
		if x.ID != t.ID && len(h) < 30 {
			h = append(h, x)
		}
	}
	m.history = h
}

// PlayID — включить конкретное видео (из истории или по ссылке).
func (m *Music) PlayID(ctx context.Context, id string) (Track, error) {
	m.requestMu.Lock()
	defer m.requestMu.Unlock()
	m.mu.Lock()
	m.mix, m.results, m.nextSeed = nil, nil, ""
	m.personal = false
	m.mu.Unlock()
	t, err := m.start(ctx, Track{ID: id})
	if err == nil {
		m.mu.Lock()
		m.personal = listenScore(t) > 0
		if m.personal {
			m.results = nil
		}
		m.mu.Unlock()
	}
	return t, err
}

// youtubeID — id видео из ссылки youtu.be/…, youtube.com/watch?v=…, /shorts/…
func youtubeID(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"youtu.be/", "v=", "/shorts/", "/embed/", "/live/"} {
		if i := strings.Index(s, p); i >= 0 {
			id := s[i+len(p):]
			if j := strings.IndexAny(id, "?&#/"); j >= 0 {
				id = id[:j]
			}
			if len(id) == 11 {
				return id
			}
		}
	}
	return ""
}

func (w *Web) getMusic(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, 200, w.musicNow())
}

// musicNow — что играет на колонке: YouTube Джарвиса или музыка Алисы.
func (w *Web) musicNow() MusicState {
	m, a := w.engine.music, w.engine.alice
	if m != nil && m.Playing() {
		return m.Snapshot()
	}
	if w.engine.spotify.Playing() {
		return w.engine.spotify.Snapshot()
	}
	if m != nil && m.Active() && !a.Playing() {
		return m.Snapshot()
	}
	if a.Active() {
		s := a.Snapshot()
		if m != nil {
			s.History = m.Snapshot().History
		}
		return s
	}
	if w.engine.spotify.Active() { // Spotify на паузе и больше ничего нет
		return w.engine.spotify.Snapshot()
	}
	if m == nil {
		return MusicState{Source: "youtube"}
	}
	return m.Snapshot()
}

// spotifyPanelActions — кнопки плеера панели → действия Spotify.Control; «стоп» у Soloist — пауза (stop.sh гасит сам сервис).
var spotifyPanelActions = map[string]string{"pause": "pause", "resume": "resume", "next": "next", "prev": "prev", "stop": "pause"}

func (w *Web) postMusic(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string  `json:"action"` // play | id | pause | resume | next | stop | seek | queue | unqueue | radio | spotify (query: liked | dj | radio)
		Query  string  `json:"query"`
		Sec    float64 `json:"sec"`
		Index  int     `json:"index"`
		On     bool    `json:"on"`
	}
	if readJSON(r, &req) != nil || w.engine.music == nil {
		bad(rw, "не понял команду")
		return
	}
	m := w.engine.music
	if req.Action == "spotify" { // кнопки «Любимые», «DJ», «Радио по треку»
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if msg := w.engine.playSpotifyKind(ctx, req.Query); !strings.HasPrefix(msg, "Включаю") {
			bad(rw, strings.TrimPrefix(msg, "Не получилось: "))
			return
		}
		writeJSON(rw, 200, w.musicNow())
		return
	}
	if sp := w.engine.spotify; !m.Playing() && w.engine.spotifyOwns() {
		if req.Action == "seek" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := sp.Seek(ctx, req.Sec); err != nil {
				bad(rw, err.Error())
				return
			}
			_, _ = sp.Now(ctx)
			writeJSON(rw, 200, w.musicNow())
			return
		}
		if act, ok := spotifyPanelActions[req.Action]; ok {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := sp.Control(ctx, act); err != nil {
				bad(rw, err.Error())
				return
			}
			_, _ = sp.Now(ctx) // сразу обновить, не ждать опроса
			writeJSON(rw, 200, w.musicNow())
			return
		}
	}
	if a := w.engine.alice; !m.Playing() && a.Active() && (a.Playing() || !m.Active()) {
		switch req.Action {
		case "pause", "resume", "next", "prev", "stop", "seek":
			if err := a.Control(req.Action, req.Sec); err != nil {
				bad(rw, err.Error())
				return
			}
			time.Sleep(400 * time.Millisecond)
			writeJSON(rw, 200, w.musicNow())
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	var err error
	switch req.Action {
	case "play":
		if id := youtubeID(req.Query); id != "" {
			_, err = m.PlayID(ctx, id)
		} else if strings.TrimSpace(req.Query) == "" {
			err = errors.New("что включить?")
		} else {
			_, err = m.Play(ctx, req.Query)
		}
	case "id":
		_, err = m.PlayID(ctx, req.Query)
	case "pause":
		m.Pause()
	case "resume":
		err = m.Resume()
	case "next":
		_, err = m.Next(ctx)
	case "stop":
		m.Stop()
	case "seek":
		err = m.Seek(req.Sec, true)
	case "queue":
		if strings.TrimSpace(req.Query) == "" {
			err = errors.New("что добавить?")
		} else {
			_, err = m.Enqueue(ctx, req.Query)
		}
	case "unqueue":
		m.Unqueue(req.Index)
	case "radio":
		err = w.store.Update(func(s *Settings) error { s.RadioOff = !req.On; return nil })
	default:
		err = errors.New("неизвестная команда")
	}
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, w.musicNow())
}
