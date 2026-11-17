package main

import (
	"context"
	"net/http"
	"time"
)

type XiaomiAuth struct {
	UserID       string `json:"user_id,omitempty"`
	CUserID      string `json:"cuser_id,omitempty"`
	SSecurity    string `json:"ssecurity,omitempty"`
	PassToken    string `json:"pass_token,omitempty"`
	ServiceToken string `json:"service_token,omitempty"`
	Region       string `json:"region,omitempty"` // cn | de | ru | us | sg | i2 …: сервер, где лежат устройства
}

func (a XiaomiAuth) SignedIn() bool {
	return a.UserID != "" && a.SSecurity != "" && a.ServiceToken != ""
}

// XiaomiProp / XiaomiAction — что умеет устройство (из спецификации MIoT).
type XiaomiProp struct {
	Name   string   `json:"name"` // «light.brightness»
	SIID   int      `json:"siid"`
	PIID   int      `json:"piid"`
	Format string   `json:"format"`
	Access string   `json:"access"` // r, w, rw
	Range  []any    `json:"range,omitempty"`
	Values []string `json:"values,omitempty"` // «1=Sweep»
	Unit   string   `json:"unit,omitempty"`
}

type XiaomiAction struct {
	Name string `json:"name"` // «vacuum.start-sweep»
	SIID int    `json:"siid"`
	AIID int    `json:"aiid"`
	In   []int  `json:"in,omitempty"`
}

type XiaomiDevice struct {
	DID     string         `json:"did"`
	Name    string         `json:"name"`
	Room    string         `json:"room,omitempty"`
	Region  string         `json:"region,omitempty"` // сервер Xiaomi, где живёт устройство (cn, de, ru…)
	Model   string         `json:"model"`
	Type    string         `json:"type,omitempty"` // light, vacuum, outlet…
	Online  bool           `json:"online"`
	Props   []XiaomiProp   `json:"props,omitempty"`
	Actions []XiaomiAction `json:"actions,omitempty"`
}

// Xiaomi forwards to its optional native provider; vendor protocols are not linked into the core.
type Xiaomi struct{ store *Store }

func (t *Xiaomi) ready() bool {
	if t == nil || !t.store.Get().Xiaomi.SignedIn() {
		return false
	}
	_, err := pluginHost.Provider("xiaomi")
	return err == nil
}
func (t *Xiaomi) call(ctx context.Context, op string, args, out any) error {
	return serviceCall(ctx, "xiaomi", "xiaomi", map[string]any{"op": op, "args": args}, out)
}
func (t *Xiaomi) invoke(op string, args, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.call(ctx, op, args, out)
}
func (t *Xiaomi) Refresh(ctx context.Context) (out int, err error) {
	err = t.call(ctx, "refresh", nil, &out)
	return
}
func (t *Xiaomi) Devices() (out []XiaomiDevice) {
	if t == nil {
		return nil
	}
	_ = t.invoke("devices", nil, &out)
	return
}
func (t *Xiaomi) QRImage() (out []byte) { _ = t.invoke("qr", nil, &out); return }
func (t *Xiaomi) loginState() map[string]any {
	out := map[string]any{}
	if err := t.invoke("login-state", nil, &out); err != nil {
		out["error"] = err.Error()
	}
	return out
}
func (t *Xiaomi) state() map[string]any {
	out := map[string]any{}
	if err := t.invoke("state", nil, &out); err != nil {
		out["error"] = err.Error()
		out["login"] = map[string]any{"error": err.Error()}
	}
	return out
}
func (t *Xiaomi) Logout() error {
	_ = t.invoke("logout", nil, nil)
	return forgetHomeAccount(t.store, "xiaomi")
}
func (t *Xiaomi) context() (out string) {
	if !t.ready() {
		return ""
	}
	_ = t.invoke("context", nil, &out)
	return
}
func (t *Xiaomi) control(ctx context.Context, args string) string {
	var out string
	if err := t.call(ctx, "control", args, &out); err != nil {
		return err.Error()
	}
	return out
}
func (t *Xiaomi) BeginLogin() error { return t.invoke("login", nil, nil) }

func xiaomiDefinition() map[string]any {
	return map[string]any{"type": "function", "name": "xiaomi_home",
		"description": "Устройства Xiaomi Home (Mi Home): узнать состояние, изменить свойство (включить, яркость, режим, температура…) или выполнить действие (пылесос: start-sweep, stop-sweeping, start-charge…). " +
			"Пылесос убирает любую комнату дома, в какой бы комнате Mi Home он ни числился: «убери в спальне» — прочитай vacuum.room-information (get), " +
			"найди id комнаты (имена бывают по-английски: Bedroom — спальня, Kitchen — кухня, Living room — гостиная, Corridor — коридор, Bathroom — ванная) " +
			"и вызови действие vacuum.start-vacuum-room-sweep (у других моделей — похожее действие уборки комнат из списка действий) с params [\"id\"]. Не говори, что пылесос «в другой комнате» и туда не доедет.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"did":    map[string]any{"type": "string", "description": "did устройства из списка"},
			"action": map[string]any{"type": "string", "enum": []string{"get", "set", "do"}, "description": "get — прочитать свойства, set — изменить свойство, do — выполнить действие"},
			"name":   map[string]any{"type": "string", "description": "свойство («light.on», «light.brightness») или действие («vacuum.start-sweep»); для get можно не указывать — прочитаются все"},
			"value":  map[string]any{"description": "новое значение для set: true/false, число или код из списка"},
			"params": map[string]any{"type": "array", "items": map[string]any{}, "description": "входные параметры действия, если нужны"}},
			"required": []string{"did", "action"}}}
}
func (w *Web) xiaomiState() map[string]any {
	st := w.engine.tools.Xiaomi.state()
	st["signed_in"] = w.store.Get().Xiaomi.SignedIn()
	return st
}

func (w *Web) xiaomiLogin(rw http.ResponseWriter, r *http.Request) {
	if err := w.engine.tools.Xiaomi.BeginLogin(); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Web) xiaomiQR(rw http.ResponseWriter, r *http.Request) {
	img := w.engine.tools.Xiaomi.QRImage()
	if len(img) == 0 {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", http.DetectContentType(img))
	rw.Header().Set("Cache-Control", "no-store")
	_, _ = rw.Write(img)
}

func (w *Web) xiaomiRefresh(rw http.ResponseWriter, r *http.Request) {
	n, err := w.engine.tools.Xiaomi.Refresh(r.Context())
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true, "devices": n})
}

func (w *Web) xiaomiLogout(rw http.ResponseWriter, r *http.Request) {
	if err := w.engine.tools.Xiaomi.Logout(); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}
