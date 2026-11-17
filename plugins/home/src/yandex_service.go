package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/newfpv/YaJavis/pluginsdk"
	"strings"
	"sync"
	"time"
)

func registerYandex(p *pluginsdk.Plugin) {
	var mu sync.Mutex
	var key string
	var current *YandexHome
	p.HandleContext("yandexhome", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Op    string          `json:"op"`
			Args  json.RawMessage `json:"args"`
			Token string          `json:"token"`
			Room  string          `json:"room"`
			Alice bool            `json:"alice"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		b, _ := json.Marshal([]any{in.Token, in.Room, in.Alice})
		mu.Lock()
		if current == nil || key != string(b) {
			token, room, alice := in.Token, in.Room, in.Alice
			current = &YandexHome{token: func() string { return token }, Room: func() string { return room }, Use: func(ch string) bool { return ch != "alice" || alice }, client: p.HTTPClientFor("yandex", 10*time.Second)}
			key = string(b)
		}
		h := current
		mu.Unlock()
		var text string
		_ = json.Unmarshal(in.Args, &text)
		switch in.Op {
		case "fast":
			ok, answer := h.Fast(ctx, text)
			return map[string]any{"handled": ok, "answer": answer}, nil
		case "has-catalog":
			return h.hasCatalog(), nil
		case "prewarm":
			h.Prewarm()
			return true, nil
		case "context":
			return h.Context(), nil
		case "control":
			var args homeArgs
			if json.Unmarshal([]byte(text), &args) != nil {
				return "Не понял команду.", nil
			}
			return h.Apply(ctx, &args), nil
		case "scenario-match":
			c, err := h.Catalog(ctx)
			if err != nil {
				return false, nil
			}
			for _, sc := range c.Scenarios {
				for _, ph := range append(scenarioPhrases(sc), sc.Name) {
					if s := normText(ph); s != "" && s == strings.TrimSpace(text) {
						return true, nil
					}
				}
			}
			return false, nil
		}
		return nil, errors.New("неизвестная команда Яндекс Умного дома")
	})
}
