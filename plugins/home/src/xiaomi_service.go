package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/newfpv/YaJavis/pluginsdk"
	"net/http"
	"time"
)

func registerXiaomi(p *pluginsdk.Plugin) func(pluginsdk.Init) {
	store := &Store{sdk: p, service: "xiaomi"}
	var t *Xiaomi
	p.HandleContext("xiaomi", func(ctx context.Context, raw json.RawMessage) (any, error) {
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
			return true, t.BeginLogin()
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

		}
		return nil, errors.New("неизвестная команда Xiaomi Home")
	})
	return func(in pluginsdk.Init) {
		migrate(p, in, "xiaomi")
		t = &Xiaomi{store: store, dataDir: in.Data, client: func() *http.Client { return p.HTTPClientFor("xiaomi", 0) }}
		p.Status("Готов к подключению Xiaomi Home")
	}
}
