package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/newfpv/YaJavis/pluginsdk"
	"net/http"
	"time"
)

func registerSmartlife(p *pluginsdk.Plugin) func(pluginsdk.Init) {
	store := &Store{sdk: p, service: "smartlife"}
	var t *SmartLife
	p.HandleContext("smartlife", func(ctx context.Context, raw json.RawMessage) (any, error) {
		if t == nil {
			return nil, errors.New("плагин запускается")
		}
		var in struct {
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var text string
		_ = json.Unmarshal(in.Args, &text)
		switch in.Op {
		case "login":
			return true, t.BeginLogin(text)
		case "logout":
			t.Logout()
			return true, nil
		case "login-state":
			return t.loginState(), nil
		case "state":
			return t.state(), nil
		case "qr":
			return t.QRImage(), nil
		case "refresh":
			return t.Refresh(ctx)
		case "devices":
			return t.Devices(), nil
		case "context":
			return t.context(), nil
		case "control":
			return t.control(ctx, text), nil
		case "fast":
			ok, answer := t.Fast(ctx, text)
			return map[string]any{"handled": ok, "answer": answer}, nil
		}
		return nil, errors.New("неизвестная команда Smart Life")
	})
	return func(in pluginsdk.Init) {
		migrate(p, in, "smartlife")
		lan := &TuyaLAN{}
		t = &SmartLife{store: store, dataDir: in.Data, lan: lan, client: func() *http.Client { return p.HTTPClientFor("smartlife", 30*time.Second) }}
		if in.Info["probe"] != true {
			go lan.Listen(func() bool { return store.Get().Tuya.SignedIn() })
		}
		p.Status("Готов к подключению Smart Life")
	}
}
