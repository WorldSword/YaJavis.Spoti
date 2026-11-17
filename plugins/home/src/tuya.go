package main

// Smart Life (Tuya) напрямую, без Алисы: вход кодом пользователя и QR-кодом из приложения Smart Life (тот же
// канал, что у интеграции Tuya в Home Assistant), устройства и их функции из облака Tuya. Команды идут по
// домашней сети (tuyalocal.go) — так свет включается за доли секунды; не вышло — через облако Tuya.
// Протокол облака описан в открытом tuya-device-sharing-sdk (MIT), код свой.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"
)

const (
	tuyaClientID = "HA_3y9q4ak7g4ephrvke" // приложение «Home Assistant» у Tuya
	tuyaSchema   = "haauthorize"
	tuyaLoginAPI = "https://apigw.iotbing.com"
)

// TuyaAuth — вход в Smart Life (в настройках колонки).
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

type SmartLife struct {
	store   *Store
	client  func() *http.Client
	dataDir string
	lan     *TuyaLAN

	mu      sync.Mutex
	devices []TuyaDevice
	loaded  bool
	login   tuyaLogin
	refresh sync.Mutex
}

type tuyaLogin struct {
	Code, Err string
	At        int64
	cancel    context.CancelFunc
}

func (t *SmartLife) ready() bool { return t != nil && t.store.Get().Tuya.SignedIn() }

// ---------------------------------------------------------------- вход

// BeginLogin — по коду пользователя Tuya выдаёт QR-код; его сканируют в Smart Life, колонка ждёт входа.
func (t *SmartLife) BeginLogin(userCode string) error {
	userCode = strings.TrimSpace(userCode)
	if userCode == "" {
		return errors.New("вставьте код пользователя из Smart Life")
	}
	t.mu.Lock()
	if t.login.cancel != nil {
		t.login.cancel()
	}
	t.login = tuyaLogin{}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q := url.Values{"clientid": {tuyaClientID}, "usercode": {userCode}, "schema": {tuyaSchema}}
	var res struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		Result  struct {
			QRCode string `json:"qrcode"`
		} `json:"result"`
	}
	if err := t.plain(ctx, http.MethodPost, tuyaLoginAPI+"/v1.0/m/life/home-assistant/qrcode/tokens?"+q.Encode(), &res); err != nil {
		return err
	}
	if !res.Success || res.Result.QRCode == "" {
		return fmt.Errorf("Smart Life не принял код пользователя: %s", orDash(res.Msg))
	}
	lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.mu.Lock()
	t.login = tuyaLogin{Code: res.Result.QRCode, At: time.Now().UnixMilli(), cancel: lcancel}
	t.mu.Unlock()
	go t.waitLogin(lctx, lcancel, userCode, res.Result.QRCode)
	return nil
}

func (t *SmartLife) waitLogin(ctx context.Context, cancel context.CancelFunc, userCode, code string) {
	defer cancel()
	q := url.Values{"clientid": {tuyaClientID}, "usercode": {userCode}}
	for {
		select {
		case <-ctx.Done():
			t.mu.Lock()
			if t.login.Code == code {
				t.login = tuyaLogin{Err: "QR-код устарел — нажмите «Войти» ещё раз"}
			}
			t.mu.Unlock()
			return
		case <-time.After(2500 * time.Millisecond):
		}
		var res struct {
			Success bool  `json:"success"`
			T       int64 `json:"t"`
			Result  struct {
				Endpoint     string `json:"endpoint"`
				TerminalID   string `json:"terminal_id"`
				UID          string `json:"uid"`
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				ExpireTime   int64  `json:"expire_time"`
			} `json:"result"`
		}
		rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
		err := t.plain(rctx, http.MethodGet, tuyaLoginAPI+"/v1.0/m/life/home-assistant/qrcode/tokens/"+url.PathEscape(code)+"?"+q.Encode(), &res)
		rcancel()
		if err != nil || !res.Success || res.Result.RefreshToken == "" {
			continue
		}
		r := res.Result
		auth := TuyaAuth{UserCode: userCode, Endpoint: r.Endpoint, TerminalID: r.TerminalID, UID: r.UID,
			AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, Expire: res.T + r.ExpireTime*1000}
		_ = t.store.Update(func(s *Settings) error {
			s.Tuya = auth
			s.justConnected = true
			return nil
		})
		t.mu.Lock()
		t.login = tuyaLogin{}
		t.mu.Unlock()
		log.Printf("Smart Life: вход выполнен, ищу устройства")
		if n, err := t.Refresh(context.Background()); err != nil {
			log.Printf("Smart Life: устройства не получены: %v", err)
		} else {
			log.Printf("Smart Life: устройств — %d", n)
		}
		return
	}
}

func (t *SmartLife) plain(ctx context.Context, method, u string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, method, u, nil)
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Smart Life ответил %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (t *SmartLife) loginState() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{"code": t.login.Code != "", "error": t.login.Err, "at": t.login.At}
}

// QRImage — PNG с QR-кодом для приложения Smart Life.
func (t *SmartLife) QRImage() []byte {
	t.mu.Lock()
	code := t.login.Code
	t.mu.Unlock()
	if code == "" {
		return nil
	}
	c, err := qr.Encode("tuyaSmart--qrLogin?token="+code, qr.Q)
	if err != nil {
		return nil
	}
	c.Scale = 8
	return c.PNG()
}

func (t *SmartLife) Logout() {
	t.mu.Lock()
	if t.login.cancel != nil {
		t.login.cancel()
	}
	t.login, t.devices, t.loaded = tuyaLogin{}, nil, true
	t.mu.Unlock()
	_ = t.store.Update(func(s *Settings) error { s.Tuya = TuyaAuth{}; return nil })
	_ = os.Remove(filepath.Join(t.dataDir, "tuya-devices.json"))
}

// ---------------------------------------------------------------- облако Tuya (шифрованные запросы)

func tuyaNonce(n int) []byte {
	const abc = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = abc[int(b[i])%len(abc)]
	}
	return b
}

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// tuyaSecret — ключ AES одного запроса: HMAC-SHA256(rid, md5(rid+refresh)) → первые 16 hex-символов.
func tuyaSecret(rid, hashKey string) []byte {
	m := hmac.New(sha256.New, []byte(rid))
	m.Write([]byte(hashKey))
	return []byte(hex.EncodeToString(m.Sum(nil))[:16])
}

func tuyaEncrypt(plain, secret []byte) (string, error) {
	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := tuyaNonce(12)
	return base64.StdEncoding.EncodeToString(nonce) + base64.StdEncoding.EncodeToString(g.Seal(nil, nonce, plain, nil)), nil
}

func tuyaDecrypt(enc string, secret []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < 28 {
		return nil, errors.New("Smart Life: непонятный ответ")
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, raw[:12], raw[12:], nil)
}

func tuyaSign(hashKey, query, body string, h map[string]string) string {
	var parts []string
	for _, k := range []string{"X-appKey", "X-requestId", "X-sid", "X-time", "X-token"} {
		if v := h[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	m := hmac.New(sha256.New, []byte(hashKey))
	m.Write([]byte(strings.Join(parts, "||") + query + body))
	return hex.EncodeToString(m.Sum(nil))
}

// api — запрос к облаку Tuya. Параметры и тело шифруются, ответ расшифровывается.
func (t *SmartLife) api(ctx context.Context, method, path string, params, body map[string]any, out any) error {
	if path != "" && !strings.HasPrefix(path, "/v1.0/m/token/") {
		if err := t.refreshToken(ctx); err != nil {
			return err
		}
	}
	a := t.store.Get().Tuya
	if !a.SignedIn() {
		return errors.New("Smart Life не подключён")
	}
	rid := uuid4()
	sum := md5.Sum([]byte(rid + a.RefreshToken))
	hashKey := hex.EncodeToString(sum[:])
	secret := tuyaSecret(rid, hashKey)
	u := strings.TrimRight(a.Endpoint, "/") + path
	var query, bodyEnc string
	if len(params) > 0 {
		js, _ := json.Marshal(params)
		enc, err := tuyaEncrypt(js, secret)
		if err != nil {
			return err
		}
		query = enc
		u += "?encdata=" + url.QueryEscape(enc)
	}
	var rd io.Reader
	if len(body) > 0 {
		js, _ := json.Marshal(body)
		enc, err := tuyaEncrypt(js, secret)
		if err != nil {
			return err
		}
		bodyEnc = enc
		b, _ := json.Marshal(map[string]string{"encdata": enc})
		rd = bytes.NewReader(b)
	}
	h := map[string]string{"X-appKey": tuyaClientID, "X-requestId": rid, "X-sid": "", "X-time": fmt.Sprint(time.Now().UnixMilli()), "X-token": a.AccessToken}
	req, _ := http.NewRequestWithContext(ctx, method, u, rd)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-sign", tuyaSign(hashKey, query, bodyEnc, h))
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Smart Life ответил %d", resp.StatusCode)
	}
	var env struct {
		Success bool            `json:"success"`
		Code    any             `json:"code"`
		Msg     string          `json:"msg"`
		T       int64           `json:"t"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&env); err != nil {
		return err
	}
	if !env.Success {
		return fmt.Errorf("Smart Life: %s (код %v)", orDash(env.Msg), env.Code)
	}
	if out == nil || len(env.Result) == 0 || string(env.Result) == "null" {
		return nil
	}
	var enc string
	if json.Unmarshal(env.Result, &enc) == nil {
		plain, err := tuyaDecrypt(enc, secret)
		if err != nil {
			return err
		}
		if p, ok := out.(*tuyaTokenResult); ok {
			p.T = env.T
		}
		return json.Unmarshal(plain, out)
	}
	return json.Unmarshal(env.Result, out)
}

type tuyaTokenResult struct {
	T            int64  `json:"-"`
	ExpireTime   int64  `json:"expireTime"`
	UID          string `json:"uid"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// refreshToken — доступ живёт около двух часов; за минуту до конца берём новый.
func (t *SmartLife) refreshToken(ctx context.Context) error {
	t.refresh.Lock()
	defer t.refresh.Unlock()
	a := t.store.Get().Tuya
	if !a.SignedIn() {
		return errors.New("Smart Life не подключён")
	}
	if a.Expire-60_000 > time.Now().UnixMilli() {
		return nil
	}
	var r tuyaTokenResult
	if err := t.api(ctx, http.MethodGet, "/v1.0/m/token/"+a.RefreshToken, nil, nil, &r); err != nil {
		if strings.Contains(err.Error(), "код") {
			return errors.New("вход в Smart Life истёк — войдите заново")
		}
		return err
	}
	if r.AccessToken == "" {
		return errors.New("Smart Life не продлил вход")
	}
	tm := r.T
	if tm == 0 {
		tm = time.Now().UnixMilli()
	}
	return t.store.Update(func(s *Settings) error {
		s.Tuya.AccessToken, s.Tuya.RefreshToken, s.Tuya.Expire = r.AccessToken, r.RefreshToken, tm+r.ExpireTime*1000
		if r.UID != "" {
			s.Tuya.UID = r.UID
		}
		return nil
	})
}

// ---------------------------------------------------------------- устройства

func (t *SmartLife) Refresh(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var homes []struct {
		OwnerID any    `json:"ownerId"`
		Name    string `json:"name"`
	}
	if err := t.api(ctx, http.MethodGet, "/v1.0/m/life/users/homes", nil, nil, &homes); err != nil {
		return 0, err
	}
	var out []TuyaDevice
	seen := map[string]bool{}
	for _, h := range homes {
		var list []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			LocalKey string `json:"local_key"`
			Category string `json:"category"`
			Product  string `json:"product_name"`
			Online   bool   `json:"online"`
			Sub      bool   `json:"sub"`
		}
		if err := t.api(ctx, http.MethodGet, "/v1.0/m/life/ha/home/devices", map[string]any{"homeId": fmt.Sprint(h.OwnerID)}, nil, &list); err != nil {
			return 0, err
		}
		for _, d := range list {
			if seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			dev := TuyaDevice{ID: d.ID, Name: d.Name, Category: d.Category, Product: d.Product, Online: d.Online}
			if !d.Sub { // устройства через шлюз (Zigbee) по сети напрямую не управляются
				dev.LocalKey = d.LocalKey
			}
			t.describe(ctx, &dev)
			out = append(out, dev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Room+out[i].Name < out[j].Room+out[j].Name })
	t.save(out)
	return len(out), nil
}

func (t *SmartLife) describe(ctx context.Context, d *TuyaDevice) {
	var spec struct {
		Functions []struct {
			Code   string `json:"code"`
			Type   string `json:"type"`
			Values string `json:"values"`
		} `json:"functions"`
		Status []struct {
			Code string `json:"code"`
		} `json:"status"`
	}
	_ = t.api(ctx, http.MethodGet, "/v1.1/m/life/"+d.ID+"/specifications", nil, nil, &spec)
	var st struct {
		DPs []struct {
			DPID      int    `json:"dpId"`
			Code      string `json:"statusCode"`
			Local     bool   `json:"supportLocal"`
			ValueConv string `json:"valueConvert"`
			ValueType string `json:"valueType"`
		} `json:"dpStatusRelationDTOS"`
	}
	dp := map[string]int{}
	if t.api(ctx, http.MethodGet, "/v1.0/m/life/devices/"+d.ID+"/status", nil, nil, &st) == nil {
		for _, x := range st.DPs {
			if x.Local && (x.ValueConv == "" || x.ValueConv == "default") {
				dp[x.Code] = x.DPID
			}
		}
	}
	funcs := map[string]bool{}
	for _, f := range spec.Functions {
		funcs[f.Code] = true
		vals := f.Values
		if len(vals) > 300 {
			vals = vals[:300]
		}
		d.Funcs = append(d.Funcs, TuyaFunc{Code: f.Code, Type: f.Type, Values: vals, DP: dp[f.Code]})
	}
	for _, s := range spec.Status {
		if !funcs[s.Code] {
			d.Status = append(d.Status, s.Code)
		}
	}
	var room struct {
		Name string `json:"name"`
	}
	if t.api(ctx, http.MethodGet, "/v1.0/m/thing/ha/"+d.ID+"/room", nil, nil, &room) == nil {
		d.Room = room.Name
	}
}

func (t *SmartLife) save(devs []TuyaDevice) {
	t.mu.Lock()
	t.devices, t.loaded = devs, true
	t.mu.Unlock()
	if b, err := json.Marshal(devs); err == nil {
		_ = os.WriteFile(filepath.Join(t.dataDir, "tuya-devices.json"), b, 0o600) // там локальные ключи устройств
	}
}

func (t *SmartLife) Devices() []TuyaDevice {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.loaded {
		t.loaded = true
		if b, err := os.ReadFile(filepath.Join(t.dataDir, "tuya-devices.json")); err == nil {
			_ = json.Unmarshal(b, &t.devices)
		}
	}
	return t.devices
}

// ---------------------------------------------------------------- управление

// Set — изменить функции устройства: сначала по домашней сети, не вышло — через облако Tuya.
func (t *SmartLife) Set(ctx context.Context, d TuyaDevice, cmds map[string]any) (string, error) {
	t0 := time.Now()
	if t.lan != nil && d.LocalKey != "" {
		dps := map[string]any{}
		for code, v := range cmds {
			for _, f := range d.Funcs {
				if f.Code == code && f.DP > 0 {
					dps[fmt.Sprint(f.DP)] = v
				}
			}
		}
		if len(dps) == len(cmds) {
			lctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			err := t.lan.Control(lctx, d.ID, d.LocalKey, dps)
			cancel()
			if err == nil {
				return fmt.Sprintf("по сети, %d мс", time.Since(t0).Milliseconds()), nil
			}
			log.Printf("Smart Life: «%s» по сети не вышло (%v) — через облако", d.Name, err)
		}
	}
	var list []map[string]any
	for code, v := range cmds {
		list = append(list, map[string]any{"code": code, "value": v})
	}
	if err := t.api(ctx, http.MethodPost, "/v1.1/m/thing/"+d.ID+"/commands", nil, map[string]any{"commands": list}, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("через облако, %d мс", time.Since(t0).Milliseconds()), nil
}

func (t *SmartLife) status(ctx context.Context, d TuyaDevice) (map[string]any, error) {
	var res []struct {
		Status []struct {
			Code  string `json:"code"`
			Value any    `json:"value"`
		} `json:"status"`
	}
	if err := t.api(ctx, http.MethodGet, "/v1.0/m/life/ha/devices/detail", map[string]any{"devIds": d.ID}, nil, &res); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, r := range res {
		for _, s := range r.Status {
			out[s.Code] = s.Value
		}
	}
	return out, nil
}

func (t *SmartLife) find(id string) (TuyaDevice, bool) {
	for _, d := range t.Devices() {
		if d.ID == id || strings.EqualFold(d.Name, id) {
			return d, true
		}
	}
	return TuyaDevice{}, false
}

// ---------------------------------------------------------------- для модели

func (t *SmartLife) context() string {
	if !t.ready() {
		return ""
	}
	devs := t.Devices()
	if len(devs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nУстройства Smart Life (управление — инструмент smart_life; id и коды функций ровно как здесь, значения — по типу и диапазону):")
	for _, d := range devs {
		state := "в сети"
		if !d.Online {
			state = "не в сети"
		}
		fmt.Fprintf(&b, "\n- id %s «%s» (%s, %s, %s)", d.ID, d.Name, orDash(d.Room), orDash(d.Product), state)
		var fs []string
		for _, f := range d.Funcs {
			s := f.Code + " [" + f.Type
			if f.Values != "" && f.Values != "{}" && len(f.Values) <= 160 {
				s += " " + f.Values
			}
			fs = append(fs, s+"]")
		}
		if len(fs) > 0 {
			b.WriteString(": " + strings.Join(fs, "; "))
		}
		if len(d.Status) > 0 {
			b.WriteString("; показывает: " + strings.Join(d.Status, ", "))
		}
	}
	return b.String()
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

func (t *SmartLife) control(ctx context.Context, args string) string {
	var a struct {
		ID       string         `json:"id"`
		Action   string         `json:"action"`
		Commands map[string]any `json:"commands"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return "Не понял команду для Smart Life."
	}
	d, ok := t.find(a.ID)
	if !ok {
		return "Такого устройства Smart Life нет в списке."
	}
	if a.Action == "get" {
		st, err := t.status(ctx, d)
		if err != nil {
			return "Smart Life не ответил: " + shortErr(err)
		}
		b, _ := json.Marshal(st)
		return "«" + d.Name + "»: " + string(b)
	}
	if len(a.Commands) == 0 {
		return "Что изменить? Передай commands."
	}
	how, err := t.Set(ctx, d, a.Commands)
	if err != nil {
		return "Smart Life не выполнил: " + shortErr(err)
	}
	log.Printf("Smart Life: «%s» %v (%s)", d.Name, a.Commands, how)
	return "Готово: «" + d.Name + "»."
}

// ---------------------------------------------------------------- быстрые команды без модели

var tuyaOnWords = map[string]bool{"включи": true, "зажги": true, "вруби": true, "включить": true, "вкл": true}
var tuyaOffWords = map[string]bool{"выключи": true, "погаси": true, "выруби": true, "выключить": true, "отключи": true, "потуши": true}

// слова, которые в названии устройства ничего не значат («в зале», «пожалуйста»)
var tuyaFiller = map[string]bool{"в": true, "во": true, "на": true, "у": true, "и": true, "пожалуйста": true, "джарвис": true, "мне": true, "там": true, "тут": true, "здесь": true}

// свет вообще — без названия устройства
var tuyaLightWords = map[string]bool{"свет": true, "освещение": true, "люстру": true, "люстра": true, "лампы": true}

// stemRu — слово без окончания: «лампу», «лампа» → «ламп»; «подсветку» ≠ «свет».
func stemRu(w string) string {
	r := []rune(w)
	if len(r) > 4 {
		r = r[:len(r)-1]
		if len(r) > 5 && strings.ContainsRune("аеиоуыэюяйь", r[len(r)-1]) {
			r = r[:len(r)-1]
		}
	}
	return string(r)
}

func stems(text string) []string {
	var out []string
	for _, w := range strings.Fields(normText(text)) {
		if !tuyaFiller[w] {
			out = append(out, stemRu(w))
		}
	}
	return out
}

// sameWordRu — одно слово в разных падежах: «маша» / «машин», «лампа» / «ламп».
func sameWordRu(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	if len(ra) < 4 {
		return false
	}
	n := 0
	for n < len(ra) && ra[n] == rb[n] {
		n++
	}
	if len(ra) == 4 { // короткое имя: «маша» / «машин»
		return n >= 3
	}
	return n == len(ra) // длиннее — только целиком: «конде» ≠ «кондиционе»
}

func subset(a, b []string) bool { // все слова a есть в b
	if len(a) == 0 {
		return false
	}
	for _, x := range a {
		if !slices.ContainsFunc(b, func(y string) bool { return sameWordRu(x, y) }) {
			return false
		}
	}
	return true
}

// switchCodes — какими функциями устройство включается (у выключателя на 2–3 клавиши — все клавиши).
func (d TuyaDevice) switchCodes() []string {
	var out []string
	for _, f := range d.Funcs {
		if f.Type != "Boolean" {
			continue
		}
		if f.Code == "switch_led" || f.Code == "switch" || f.Code == "led_switch" || (strings.HasPrefix(f.Code, "switch_") && len(f.Code) <= 9 && f.Code[7] >= '1' && f.Code[7] <= '9') {
			out = append(out, f.Code)
		}
	}
	return out
}

// isTuyaLight — светит: лампы, ленты, контроллеры, настенные выключатели света.
func isTuyaLight(d TuyaDevice) bool {
	switch d.Category {
	case "dj", "dd", "xdd", "fwd", "dc", "tgq", "tgkg", "fsd", "gyd", "tyndj", "kg":
		return true
	}
	for _, f := range d.Funcs {
		if f.Code == "switch_led" {
			return true
		}
	}
	return false
}

// tuyaRoomOf — комната, названная во фразе («в спальне»), и фраза без неё.
func tuyaRoomOf(rest []string, devs []TuyaDevice) (string, []string) {
	for _, d := range devs {
		if d.Room == "" {
			continue
		}
		room := stems(d.Room)
		if subset(room, rest) {
			var left []string
			for _, w := range rest {
				if !slices.Contains(room, w) {
					left = append(left, w)
				}
			}
			return d.Room, left
		}
	}
	return "", rest
}

// tuyaPick — какие устройства имеются в виду. room — комната колонки (если комната во фразе не названа).
func tuyaPick(cmd string, devs []TuyaDevice, speakerRoom string) (on bool, picked []TuyaDevice, ok bool) {
	words := strings.Fields(normText(cmd))
	for len(words) > 0 && (words[0] == "джарвис" || words[0] == "пожалуйста") {
		words = words[1:]
	}
	if len(words) < 2 {
		return false, nil, false
	}
	on = tuyaOnWords[words[0]]
	if !on && !tuyaOffWords[words[0]] {
		return false, nil, false
	}
	rest := stems(strings.Join(words[1:], " "))
	everywhere := slices.Contains(rest, stemRu("весь")) || slices.Contains(rest, stemRu("везде")) || slices.Contains(rest, stemRu("всём")) || slices.Contains(rest, "доме")
	room, rest := tuyaRoomOf(rest, devs)
	if room == "" && !everywhere {
		room = speakerRoom
	}
	inRoom := func(d TuyaDevice) bool { return everywhere || room == "" || strings.EqualFold(d.Room, room) }
	// 1) по названию: «выключи компьютер» → «Компьютер розетка»; «включи лампу» → «Лампа»
	var byName []TuyaDevice
	for _, d := range devs {
		if len(d.switchCodes()) == 0 {
			continue
		}
		name := stems(d.Name)
		if subset(rest, name) || subset(name, rest) {
			byName = append(byName, d)
		}
	}
	if len(byName) > 1 { // одинаковые названия в разных комнатах — берём из нужной
		var here []TuyaDevice
		for _, d := range byName {
			if inRoom(d) {
				here = append(here, d)
			}
		}
		if len(here) > 0 {
			byName = here
		}
	}
	if len(byName) > 0 {
		return on, byName, true
	}
	// 2) «свет» без названия — весь свет в комнате (как у Алисы)
	generic := false
	for _, w := range rest {
		for lw := range tuyaLightWords {
			if w == stemRu(lw) {
				generic = true
			}
		}
	}
	if !generic {
		return false, nil, false
	}
	for _, d := range devs {
		if isTuyaLight(d) && len(d.switchCodes()) > 0 && inRoom(d) {
			picked = append(picked, d)
		}
	}
	return on, picked, len(picked) > 0
}

// Fast — «включи/выключи …» сразу по сети, без модели. false — это не про Smart Life.
func (t *SmartLife) Fast(ctx context.Context, cmd string) (bool, string) {
	if !t.ready() {
		return false, ""
	}
	on, picked, ok := tuyaPick(cmd, t.Devices(), t.store.Get().SpeakerRoom)
	if !ok {
		return false, ""
	}
	var done, failed []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range picked {
		wg.Add(1)
		go func(d TuyaDevice) { // все устройства разом: свет в комнате гаснет одновременно
			defer wg.Done()
			cmds := map[string]any{}
			for _, c := range d.switchCodes() {
				cmds[c] = on
			}
			how, err := t.Set(ctx, d, cmds)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				log.Printf("Smart Life: «%s»: %v", d.Name, err)
				failed = append(failed, d.Name)
				return
			}
			log.Printf("Smart Life: «%s» → %v (%s)", d.Name, on, how)
			done = append(done, d.Name)
		}(d)
	}
	wg.Wait()
	if len(done) == 0 {
		return true, "Не получилось: " + strings.Join(failed, ", ") + "."
	}
	if len(failed) > 0 {
		return true, "Готово, кроме: " + strings.Join(failed, ", ") + "."
	}
	return true, "Готово."
}
