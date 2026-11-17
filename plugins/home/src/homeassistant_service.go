package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/newfpv/YaJavis/pluginsdk"
	"net/http"
	"sync"
	"time"
)

func registerHomeassistant(p *pluginsdk.Plugin) func(pluginsdk.Init) {
	var mu sync.Mutex
	var current *HomeAssistant
	var key string
	p.HandleContext("homeassistant", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Op     string          `json:"op"`
			Config HASettings      `json:"config"`
			Args   json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(in.Config)
		mu.Lock()
		if current == nil || key != string(b) {
			c := in.Config
			current = &HomeAssistant{config: func() HASettings { return c }, client: &http.Client{Timeout: 15 * time.Second}}
			key = string(b)
		}
		h := current
		mu.Unlock()
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		var text string
		_ = json.Unmarshal(in.Args, &text)
		switch in.Op {
		case "catalog":
			var force bool
			_ = json.Unmarshal(in.Args, &force)
			return h.catalog(ctx, force)
		case "rooms":
			return h.discoverRooms(ctx)
		case "control":
			return h.control(ctx, text), nil
		case "command":
			return h.vacuumCommand(ctx, text), nil
		case "intent":
			return h.vacuumIntent(text), nil
		case "alive":
			return h.alive(), nil
		case "context":
			return h.context(), nil
		case "invalidate":
			h.mu.Lock()
			h.entities = nil
			h.fetched = time.Time{}
			h.mu.Unlock()
			return true, nil
		}
		return nil, errors.New("неизвестная команда Home Assistant")
	})
	return func(pluginsdk.Init) {}
}
