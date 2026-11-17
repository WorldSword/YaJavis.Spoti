package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (w *Web) haState() map[string]any {
	c := w.store.Get().HomeAssistant
	return map[string]any{"url": c.URL, "configured": c.URL != "" && c.Token != "", "rooms": c.Rooms}
}
func (w *Web) getHomeAssistant(rw http.ResponseWriter, r *http.Request) {
	h := w.engine.tools.HomeAssistant
	if !h.ready() {
		writeJSON(rw, 200, map[string]any{"configured": false, "entities": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	entities, err := h.catalog(ctx, true)
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"configured": true, "entities": entities})
}
func (w *Web) discoverHARooms(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	h := w.engine.tools.HomeAssistant
	if !h.ready() {
		bad(rw, "сначала подключи Home Assistant")
		return
	}
	rooms, err := h.discoverRooms(ctx)
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"rooms": rooms})
}

func (w *Web) putHomeAssistant(rw http.ResponseWriter, r *http.Request) {
	var c HASettings
	if readJSON(r, &c) != nil {
		bad(rw, "не понял настройки Home Assistant")
		return
	}
	base, err := validateHAURL(c.URL)
	if err != nil {
		bad(rw, err.Error())
		return
	}
	c.URL = base
	old := w.store.Get().HomeAssistant
	c.Token = strings.TrimSpace(c.Token)
	if c.Token == "" && c.URL == old.URL {
		c.Token = old.Token
	}
	if c.Token == "" {
		bad(rw, "вставь долгосрочный токен Home Assistant из профиля пользователя")
		return
	}
	if len(c.Rooms) > 100 {
		bad(rw, "слишком много комнат")
		return
	}
	h := &HomeAssistant{config: func() HASettings { return c }, client: &http.Client{Timeout: 12 * time.Second}}
	entities, err := h.catalog(r.Context(), true)
	if err != nil {
		bad(rw, err.Error())
		return
	}
	known := map[string]bool{}
	for _, e := range entities {
		known[e.ID] = true
	}
	for _, room := range c.Rooms {
		d, _, _ := strings.Cut(room.Entity, ".")
		if strings.TrimSpace(room.Name) == "" || !known[room.Entity] || (d != "script" && d != "notify" && d != "text") {
			bad(rw, "выбери существующий сценарий или действие уборки для каждой комнаты")
			return
		}
		if room.Vacuum != "" && (!strings.HasPrefix(room.Vacuum, "vacuum.") || !known[room.Vacuum]) {
			bad(rw, "выбранный пылесос не найден")
			return
		}
		if d != "script" && strings.TrimSpace(room.RoomIDs) == "" {
			bad(rw, "для действия уборки нужны номера комнат на карте пылесоса")
			return
		}
	}
	if err := w.store.Update(func(s *Settings) error {
		was := s.HomeConnected("homeassistant")
		s.HomeAssistant = c
		s.homeJustConnected("homeassistant", was)
		return nil
	}); err != nil {
		bad(rw, err.Error())
		return
	}
	shared := w.engine.tools.HomeAssistant
	shared.invalidate()
	writeJSON(rw, 200, w.haState())
}
func (w *Web) deleteHomeAssistant(rw http.ResponseWriter, r *http.Request) {
	if err := w.store.Update(func(s *Settings) error { s.HomeAssistant = HASettings{}; return nil }); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, w.haState())
}
