package main

import (
	"context"
	"net/http"
	"time"
)

type TuyaAuth struct {
	UserCode     string `json:"user_code,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	TerminalID   string `json:"terminal_id,omitempty"`
	UID          string `json:"uid,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Expire       int64  `json:"expire,omitempty"` // мс unix
}

func (a TuyaAuth) SignedIn() bool { return a.Endpoint != "" && a.RefreshToken != "" }

// TuyaFunc — что можно менять у устройства (код функции Tuya, тип, допустимые значения).
type TuyaFunc struct {
	Code   string `json:"code"`
	Type   string `json:"type"` // Boolean, Integer, Enum, String, Json
	Values string `json:"values,omitempty"`
	DP     int    `json:"dp,omitempty"` // номер для команды по сети (0 — только через облако)
}

type TuyaDevice struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Room     string     `json:"room,omitempty"`
	Category string     `json:"category"`
	Product  string     `json:"product,omitempty"`
	Online   bool       `json:"online"`
	LocalKey string     `json:"local_key,omitempty"`
	Funcs    []TuyaFunc `json:"funcs,omitempty"`
	Status   []string   `json:"status,omitempty"` // коды, которые только показывают состояние
}

// SmartLife forwards to its optional native provider; vendor protocols are not linked into the core.
type SmartLife struct{ store *Store }

func (t *SmartLife) ready() bool {
	if t == nil || !t.store.Get().Tuya.SignedIn() {
		return false
	}
	_, err := pluginHost.Provider("smartlife")
	return err == nil
}
func (t *SmartLife) call(ctx context.Context, op string, args, out any) error {
	return serviceCall(ctx, "smartlife", "smartlife", map[string]any{"op": op, "args": args}, out)
}
func (t *SmartLife) invoke(op string, args, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.call(ctx, op, args, out)
}
func (t *SmartLife) Refresh(ctx context.Context) (out int, err error) {
	err = t.call(ctx, "refresh", nil, &out)
	return
}
func (t *SmartLife) Devices() (out []TuyaDevice) {
	if t == nil {
		return nil
	}
	_ = t.invoke("devices", nil, &out)
	return
}
func (t *SmartLife) QRImage() (out []byte) { _ = t.invoke("qr", nil, &out); return }
func (t *SmartLife) loginState() map[string]any {
	out := map[string]any{}
	if err := t.invoke("login-state", nil, &out); err != nil {
		out["error"] = err.Error()
	}
	return out
}
func (t *SmartLife) state() map[string]any {
	out := map[string]any{}
	if err := t.invoke("state", nil, &out); err != nil {
		out["error"] = err.Error()
		out["login"] = map[string]any{"error": err.Error()}
	}
	return out
}
func (t *SmartLife) Logout() error {
	_ = t.invoke("logout", nil, nil)
	return forgetHomeAccount(t.store, "smartlife")
}
func (t *SmartLife) context() (out string) {
	if !t.ready() {
		return ""
	}
	_ = t.invoke("context", nil, &out)
	return
}
func (t *SmartLife) control(ctx context.Context, args string) string {
	var out string
	if err := t.call(ctx, "control", args, &out); err != nil {
		return err.Error()
	}
	return out
}
func (t *SmartLife) BeginLogin(code string) error { return t.invoke("login", code, nil) }
func (t *SmartLife) Fast(ctx context.Context, text string) (bool, string) {
	var out struct {
		Handled bool   `json:"handled"`
		Answer  string `json:"answer"`
	}
	if !t.ready() {
		return false, ""
	}
	if err := t.call(ctx, "fast", text, &out); err != nil {
		return true, err.Error()
	}
	return out.Handled, out.Answer
}

func smartLifeDefinition() map[string]any {
	return map[string]any{"type": "function", "name": "smart_life",
		"description": "Устройства Smart Life (Tuya): узнать состояние или изменить функции — включить/выключить (switch_led, switch_1…), яркость, цвет, режим, температуру.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"id":       map[string]any{"type": "string", "description": "id устройства из списка"},
			"action":   map[string]any{"type": "string", "enum": []string{"get", "set"}},
			"commands": map[string]any{"type": "object", "description": "для set: {код функции: значение}, например {\"switch_led\": true, \"bright_value_v2\": 500}"}},
			"required": []string{"id", "action"}}}
}
func (w *Web) smartLifeState() map[string]any {
	st := w.engine.tools.SmartLife.state()
	st["signed_in"] = w.store.Get().Tuya.SignedIn()
	return st
}

func (w *Web) smartLifeLogin(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	_ = readJSON(r, &req)
	if err := w.engine.tools.SmartLife.BeginLogin(req.UserCode); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Web) smartLifeQR(rw http.ResponseWriter, r *http.Request) {
	img := w.engine.tools.SmartLife.QRImage()
	if len(img) == 0 {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "image/png")
	rw.Header().Set("Cache-Control", "no-store")
	_, _ = rw.Write(img)
}

func (w *Web) smartLifeRefresh(rw http.ResponseWriter, r *http.Request) {
	n, err := w.engine.tools.SmartLife.Refresh(r.Context())
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true, "devices": n})
}

func (w *Web) smartLifeLogout(rw http.ResponseWriter, r *http.Request) {
	if err := w.engine.tools.SmartLife.Logout(); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}
