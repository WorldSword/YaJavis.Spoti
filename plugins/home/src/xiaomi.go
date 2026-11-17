package main

// Xiaomi Home (Mi Home) напрямую, без Home Assistant: вход по QR-коду из приложения Mi Home, устройства и их
// возможности по спецификации MIoT, управление через облако Xiaomi. Протокол облака — как у приложения
// (зашифрованные запросы RC4); описан в открытом Xiaomi-cloud-tokens-extractor (MIT), код свой.

import (
	"context"
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// xnum — число Xiaomi (id дома, пользователя): приходит то числом, то строкой, отправляется числом.
type xnum string

func (n *xnum) UnmarshalJSON(b []byte) error {
	*n = xnum(strings.Trim(string(b), `"`))
	return nil
}

func (n xnum) MarshalJSON() ([]byte, error) {
	if n == "" {
		return []byte("0"), nil
	}
	return []byte(n), nil
}

// XiaomiAuth — вход в аккаунт Xiaomi (хранится в настройках колонки).
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

var xiaomiRegions = []string{"de", "ru", "cn", "us", "sg", "i2", "tw", "in"}

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

type Xiaomi struct {
	store   *Store
	client  func() *http.Client
	dataDir string

	mu      sync.Mutex
	devices []XiaomiDevice
	loaded  bool
	login   xiaomiLogin
	agent   string
}

type xiaomiLogin struct {
	QR, URL, Err string
	Busy         bool
	At           int64 // когда выдан код: картинка в панели не перезагружается при каждом опросе
	img          []byte
	cancel       context.CancelFunc
}

func (x *Xiaomi) ready() bool { return x != nil && x.store.Get().Xiaomi.SignedIn() }

func (x *Xiaomi) userAgent() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.agent == "" {
		b := make([]byte, 31)
		_, _ = rand.Read(b)
		var s strings.Builder
		for i := 0; i < 18; i++ {
			s.WriteByte('a' + b[i]%26)
		}
		s.WriteByte('-')
		for i := 18; i < 31; i++ {
			s.WriteByte('A' + b[i]%5)
		}
		x.agent = s.String() + " APP/com.xiaomi.mihome APPV/10.5.201"
	}
	return x.agent
}

func xiaomiJSON(b []byte, out any) error {
	return json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(string(b)), "&&&START&&&")), out)
}

// ---------------------------------------------------------------- вход по QR-коду

// BeginLogin — Xiaomi выдаёт QR-код; его сканируют в приложении Mi Home, колонка ждёт подтверждения.
func (x *Xiaomi) BeginLogin() error {
	x.mu.Lock()
	if x.login.cancel != nil {
		x.login.cancel()
	}
	x.login = xiaomiLogin{Busy: true}
	x.mu.Unlock()
	jar, _ := cookiejar.New(nil)
	base := x.client()
	c := &http.Client{Transport: base.Transport, Jar: jar}
	q := url.Values{"_qrsize": {"360"}, "qs": {"%3Fsid%3Dxiaomiio%26_json%3Dtrue"}, "callback": {"https://sts.api.io.mi.com/sts"},
		"_hasLogo": {"false"}, "sid": {"xiaomiio"}, "serviceParam": {""}, "_locale": {"ru_RU"}, "_dc": {fmt.Sprint(time.Now().UnixMilli())}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var start struct {
		QR      string `json:"qr"`
		URL     string `json:"loginUrl"`
		LP      string `json:"lp"`
		Timeout int    `json:"timeout"`
	}
	if err := x.get(ctx, c, "https://account.xiaomi.com/longPolling/loginUrl?"+q.Encode(), &start); err != nil || start.LP == "" {
		msg := "Xiaomi не выдал QR-код: " + errText(err)
		x.setLoginErr(msg)
		return errors.New(msg)
	}
	img, _ := x.fetch(ctx, c, start.QR)
	lctx, lcancel := context.WithTimeout(context.Background(), time.Duration(max(start.Timeout, 120))*time.Second)
	x.mu.Lock()
	x.login = xiaomiLogin{QR: start.QR, URL: start.URL, Busy: true, img: img, cancel: lcancel, At: time.Now().UnixMilli()}
	x.mu.Unlock()
	go x.waitLogin(lctx, lcancel, c, start.LP)
	return nil
}

func errText(err error) string {
	if err == nil {
		return "пустой ответ"
	}
	return shortErr(err)
}

func (x *Xiaomi) setLoginErr(e string) {
	x.mu.Lock()
	x.login = xiaomiLogin{Err: e}
	x.mu.Unlock()
}

func (x *Xiaomi) waitLogin(ctx context.Context, cancel context.CancelFunc, c *http.Client, lp string) {
	defer cancel()
	var res struct {
		UserID    xnum   `json:"userId"`
		CUserID   string `json:"cUserId"`
		SSecurity string `json:"ssecurity"`
		PassToken string `json:"passToken"`
		Location  string `json:"location"`
	}
	for {
		if ctx.Err() != nil {
			x.setLoginErr("QR-код устарел — нажмите «Войти» ещё раз")
			return
		}
		rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
		err := x.get(rctx, c, lp, &res)
		rcancel()
		if err == nil && res.SSecurity != "" {
			break
		}
		if err != nil && ctx.Err() == nil {
			time.Sleep(time.Second) // долгий опрос оборвался — спрашиваем снова
		}
	}
	token, err := x.serviceToken(ctx, c, res.Location)
	if err != nil {
		x.setLoginErr("вход подтверждён, но Xiaomi не выдал доступ: " + shortErr(err))
		return
	}
	auth := XiaomiAuth{UserID: string(res.UserID), CUserID: res.CUserID, SSecurity: res.SSecurity, PassToken: res.PassToken, ServiceToken: token}
	_ = x.store.Update(func(s *Settings) error {
		s.Xiaomi = auth
		s.justConnected = true
		return nil
	})
	log.Printf("Xiaomi Home: вход выполнен, ищу устройства")
	x.mu.Lock()
	x.login = xiaomiLogin{}
	x.mu.Unlock()
	if n, err := x.Refresh(context.Background()); err != nil {
		log.Printf("Xiaomi Home: устройства не получены: %v", err)
	} else {
		log.Printf("Xiaomi Home: устройств — %d", n)
	}
}

// serviceToken — переход по адресу из ответа входа ставит куку serviceToken.
func (x *Xiaomi) serviceToken(ctx context.Context, c *http.Client, location string) (string, error) {
	if location == "" {
		return "", errors.New("нет адреса для доступа")
	}
	var token string
	cc := *c
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if r := req.Response; r != nil {
			for _, k := range r.Cookies() {
				if k.Name == "serviceToken" && k.Value != "" {
					token = k.Value
				}
			}
		}
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	resp, err := cc.Do(req)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	for _, k := range resp.Cookies() {
		if k.Name == "serviceToken" && k.Value != "" {
			token = k.Value
		}
	}
	if token == "" && cc.Jar != nil {
		for _, host := range []string{"https://sts.api.io.mi.com/", "https://api.io.mi.com/"} {
			u, _ := url.Parse(host)
			for _, k := range cc.Jar.Cookies(u) {
				if k.Name == "serviceToken" && k.Value != "" {
					token = k.Value
				}
			}
		}
	}
	if token == "" {
		return "", fmt.Errorf("ответ %d без serviceToken", resp.StatusCode)
	}
	return token, nil
}

// renew — доступ истёк: по passToken просим новый, как приложение при запуске.
func (x *Xiaomi) renew(ctx context.Context) error {
	a := x.store.Get().Xiaomi
	if a.PassToken == "" {
		return errors.New("вход в Xiaomi истёк — войдите заново")
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://account.xiaomi.com/")
	jar.SetCookies(u, []*http.Cookie{{Name: "userId", Value: a.UserID}, {Name: "cUserId", Value: a.CUserID}, {Name: "passToken", Value: a.PassToken}})
	c := &http.Client{Transport: x.client().Transport, Jar: jar}
	var res struct {
		Code      int    `json:"code"`
		SSecurity string `json:"ssecurity"`
		Location  string `json:"location"`
		PassToken string `json:"passToken"`
	}
	if err := x.get(ctx, c, "https://account.xiaomi.com/pass/serviceLogin?sid=xiaomiio&_json=true", &res); err != nil {
		return err
	}
	if res.SSecurity == "" || res.Location == "" {
		_ = x.store.Update(func(s *Settings) error { s.Xiaomi.ServiceToken = ""; return nil })
		return errors.New("вход в Xiaomi истёк — войдите заново по QR-коду")
	}
	token, err := x.serviceToken(ctx, c, res.Location)
	if err != nil {
		return err
	}
	return x.store.Update(func(s *Settings) error {
		s.Xiaomi.SSecurity, s.Xiaomi.ServiceToken = res.SSecurity, token
		if res.PassToken != "" {
			s.Xiaomi.PassToken = res.PassToken
		}
		return nil
	})
}

func (x *Xiaomi) Logout() {
	x.mu.Lock()
	if x.login.cancel != nil {
		x.login.cancel()
	}
	x.login, x.devices, x.loaded = xiaomiLogin{}, nil, true
	x.mu.Unlock()
	_ = x.store.Update(func(s *Settings) error { s.Xiaomi = XiaomiAuth{}; return nil })
	_ = os.Remove(filepath.Join(x.dataDir, "xiaomi-devices.json"))
}

func (x *Xiaomi) loginState() map[string]any {
	x.mu.Lock()
	defer x.mu.Unlock()
	return map[string]any{"code": x.login.QR != "", "url": x.login.URL, "error": x.login.Err, "busy": x.login.Busy, "at": x.login.At}
}

// QRImage — картинка QR-кода (браузер не ходит к Xiaomi сам: страница настроек открыта только колонке).
func (x *Xiaomi) QRImage() []byte {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.login.img
}

func (x *Xiaomi) get(ctx context.Context, c *http.Client, u string, out any) error {
	b, err := x.fetch(ctx, c, u)
	if err != nil {
		return err
	}
	return xiaomiJSON(b, out)
}

func (x *Xiaomi) fetch(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	if u == "" {
		return nil, errors.New("нет адреса")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", x.userAgent())
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Xiaomi ответил %d", resp.StatusCode)
	}
	return b, nil
}

// ---------------------------------------------------------------- облако Xiaomi (зашифрованные запросы)

func xiaomiAPI(region string) string {
	if region == "cn" || region == "" {
		return "https://api.io.mi.com/app"
	}
	return "https://" + region + ".api.io.mi.com/app"
}

func xiaomiNonce() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b[:8])
	binary.BigEndian.PutUint32(b[8:], uint32(time.Now().UnixMilli()/60000))
	return base64.StdEncoding.EncodeToString(b)
}

func xiaomiSignedNonce(ssecurity, nonce string) (string, error) {
	s, err := base64.StdEncoding.DecodeString(ssecurity)
	if err != nil {
		return "", err
	}
	n, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append(s, n...))
	return base64.StdEncoding.EncodeToString(h[:]), nil
}

func xiaomiRC4(signedNonce string, data []byte) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(signedNonce)
	if err != nil {
		return nil, err
	}
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	skip := make([]byte, 1024)
	c.XORKeyStream(skip, skip)
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out, nil
}

// xiaomiSign — подпись: метод, путь без /app, параметры по порядку, подписанный nonce → SHA1.
func xiaomiSign(method, path string, keys, vals []string, signedNonce string) string {
	parts := []string{strings.ToUpper(method), path}
	for i := range keys {
		parts = append(parts, keys[i]+"="+vals[i])
	}
	parts = append(parts, signedNonce)
	h := sha1.Sum([]byte(strings.Join(parts, "&")))
	return base64.StdEncoding.EncodeToString(h[:])
}

// call — запрос к облаку Xiaomi; data — JSON-параметры. Истёк доступ — один раз обновляем и повторяем.
func (x *Xiaomi) call(ctx context.Context, region, path string, data any, out any) error {
	err := x.callOnce(ctx, region, path, data, out)
	if err != nil && errors.Is(err, errXiaomiAuth) {
		if rerr := x.renew(ctx); rerr != nil {
			return rerr
		}
		err = x.callOnce(ctx, region, path, data, out)
	}
	return err
}

var errXiaomiAuth = errors.New("Xiaomi: нет доступа")

func (x *Xiaomi) callOnce(ctx context.Context, region, path string, data any, out any) error {
	a := x.store.Get().Xiaomi
	if !a.SignedIn() {
		return errors.New("Xiaomi Home не подключён")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	nonce := xiaomiNonce()
	sn, err := xiaomiSignedNonce(a.SSecurity, nonce)
	if err != nil {
		return err
	}
	// rc4_hash__ — подпись открытых параметров; потом все значения шифруются, и подписываются уже они
	hash := xiaomiSign("POST", path, []string{"data"}, []string{string(raw)}, sn)
	keys, vals := []string{"data", "rc4_hash__"}, []string{string(raw), hash}
	for i := range vals {
		enc, err := xiaomiRC4(sn, []byte(vals[i]))
		if err != nil {
			return err
		}
		vals[i] = base64.StdEncoding.EncodeToString(enc)
	}
	sig := xiaomiSign("POST", path, keys, vals, sn)
	q := url.Values{}
	for i := range keys {
		q.Set(keys[i], vals[i])
	}
	q.Set("signature", sig)
	q.Set("ssecurity", a.SSecurity)
	q.Set("_nonce", nonce)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, xiaomiAPI(region)+path+"?"+q.Encode(), nil)
	req.Header.Set("User-Agent", x.userAgent())
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-xiaomi-protocal-flag-cli", "PROTOCAL-HTTP2")
	req.Header.Set("MIOT-ENCRYPT-ALGORITHM", "ENCRYPT-RC4")
	_, off := time.Now().Zone()
	for k, v := range map[string]string{"userId": a.UserID, "yetAnotherServiceToken": a.ServiceToken, "serviceToken": a.ServiceToken,
		"locale": "ru_RU", "timezone": fmt.Sprintf("GMT%+03d:00", off/3600), "channel": "MI_APP_STORE"} {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	resp, err := x.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return errXiaomiAuth
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("Xiaomi ответил %d", resp.StatusCode)
	}
	enc, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		return fmt.Errorf("Xiaomi: непонятный ответ")
	}
	plain, err := xiaomiRC4(sn, enc)
	if err != nil {
		return err
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(plain, &env); err != nil {
		return fmt.Errorf("Xiaomi: непонятный ответ")
	}
	if env.Code == 2 || env.Code == 3 || strings.Contains(strings.ToLower(env.Message), "auth") {
		return errXiaomiAuth
	}
	if env.Code != 0 {
		return fmt.Errorf("Xiaomi: %s (код %d)", env.Message, env.Code)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// ---------------------------------------------------------------- устройства

type xiaomiHome struct {
	ID    xnum   `json:"id"`
	Name  string `json:"name"`
	UID   xnum   `json:"uid"`
	Rooms []struct {
		Name string   `json:"name"`
		DIDs []string `json:"dids"`
	} `json:"roomlist"`
}

type xiaomiRawDevice struct {
	DID      string `json:"did"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	SpecType string `json:"spec_type"`
	Online   bool   `json:"isOnline"`
}

// Refresh — устройства со всех серверов Xiaomi сразу: у одного хозяина пылесос может жить на китайском (cn),
// а увлажнитель — на европейском (de). Каждое устройство помнит свой сервер. Возвращает число устройств.
func (x *Xiaomi) Refresh(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	var all []XiaomiDevice
	var regions []string
	var lastErr error
	seen := map[string]bool{}
	for _, region := range xiaomiRegions {
		devs, err := x.devicesIn(ctx, region)
		if err != nil {
			lastErr = err
			if errors.Is(err, errXiaomiAuth) || strings.Contains(err.Error(), "войдите") {
				return 0, err
			}
			log.Printf("Xiaomi Home: сервер %s: %v", region, err)
			continue
		}
		n := 0
		for _, d := range devs {
			if !seen[d.DID] {
				seen[d.DID] = true
				d.Region = region
				all = append(all, d)
				n++
			}
		}
		if n > 0 {
			regions = append(regions, region)
			log.Printf("Xiaomi Home: сервер %s — устройств %d", region, n)
		}
	}
	if len(all) == 0 && lastErr != nil {
		return 0, lastErr
	}
	x.describe(ctx, all)
	_ = x.store.Update(func(s *Settings) error { s.Xiaomi.Region = strings.Join(regions, ", "); return nil })
	x.save(all)
	return len(all), nil
}

func (x *Xiaomi) devicesIn(ctx context.Context, region string) ([]XiaomiDevice, error) {
	var homes struct {
		Homes []xiaomiHome `json:"homelist"`
	}
	if err := x.call(ctx, region, "/v2/homeroom/gethome", map[string]any{"fg": true, "fetch_share": true, "fetch_share_dev": true, "limit": 300, "app_ver": 7}, &homes); err != nil {
		return nil, err
	}
	var out []XiaomiDevice
	seen := map[string]bool{}
	for _, h := range homes.Homes {
		room := map[string]string{}
		for _, r := range h.Rooms {
			for _, d := range r.DIDs {
				room[d] = r.Name
			}
		}
		var list struct {
			Devices []xiaomiRawDevice `json:"device_info"`
		}
		if err := x.call(ctx, region, "/v2/home/home_device_list", map[string]any{"home_owner": h.UID, "home_id": h.ID, "limit": 200,
			"get_split_device": true, "support_smart_home": true}, &list); err != nil {
			return nil, err
		}
		for _, d := range list.Devices {
			if seen[d.DID] || d.DID == "" {
				continue
			}
			seen[d.DID] = true
			out = append(out, XiaomiDevice{DID: d.DID, Name: d.Name, Room: room[d.DID], Model: d.Model, Online: d.Online, Type: d.SpecType})
		}
	}
	return out, nil
}

// describe — по модели узнаём из спецификации MIoT, какие у устройства свойства и действия.
func (x *Xiaomi) describe(ctx context.Context, devs []XiaomiDevice) {
	c := x.client()
	specs := map[string]*miotSpec{}
	var types map[string]string // модель → urn (если в списке устройств его нет)
	for i := range devs {
		urn := devs[i].Type
		if urn == "" {
			if types == nil {
				want := map[string]bool{}
				for _, d := range devs {
					if d.Type == "" {
						want[d.Model] = true
					}
				}
				types = miotTypes(ctx, c, want)
			}
			urn = types[devs[i].Model]
		}
		devs[i].Type = ""
		if urn == "" {
			continue
		}
		sp, ok := specs[urn]
		if !ok {
			sp = miotSpecFor(ctx, c, urn)
			specs[urn] = sp
		}
		if sp == nil {
			continue
		}
		devs[i].Type = urnName(sp.Type)
		for _, s := range sp.Services {
			sname := urnName(s.Type)
			if sname == "device-information" {
				continue
			}
			for _, p := range s.Properties {
				xp := XiaomiProp{Name: sname + "." + urnName(p.Type), SIID: s.IID, PIID: p.IID, Format: p.Format, Unit: p.Unit, Range: p.Range}
				for _, a := range p.Access {
					switch a {
					case "read":
						xp.Access += "r"
					case "write":
						xp.Access += "w"
					}
				}
				if !strings.Contains(xp.Access, "r") && !strings.Contains(xp.Access, "w") {
					continue
				}
				for _, v := range p.Values {
					xp.Values = append(xp.Values, fmt.Sprintf("%v=%s", v.Value, v.Description))
				}
				devs[i].Props = append(devs[i].Props, xp)
			}
			for _, a := range s.Actions {
				devs[i].Actions = append(devs[i].Actions, XiaomiAction{Name: sname + "." + urnName(a.Type), SIID: s.IID, AIID: a.IID, In: a.In})
			}
		}
	}
}

type miotSpec struct {
	Type     string `json:"type"`
	Services []struct {
		IID        int    `json:"iid"`
		Type       string `json:"type"`
		Properties []struct {
			IID    int      `json:"iid"`
			Type   string   `json:"type"`
			Format string   `json:"format"`
			Access []string `json:"access"`
			Unit   string   `json:"unit"`
			Range  []any    `json:"value-range"`
			Values []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"value-list"`
		} `json:"properties"`
		Actions []struct {
			IID  int    `json:"iid"`
			Type string `json:"type"`
			In   []int  `json:"in"`
		} `json:"actions"`
	} `json:"services"`
}

// urnName — «urn:miot-spec-v2:property:brightness:0000000D:…» → «brightness».
func urnName(urn string) string {
	parts := strings.Split(urn, ":")
	if len(parts) > 3 {
		return parts[3]
	}
	return urn
}

func miotSpecFor(ctx context.Context, c *http.Client, urn string) *miotSpec {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://miot-spec.org/miot-spec-v2/instance?type="+url.QueryEscape(urn), nil)
	resp, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var sp miotSpec
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&sp) != nil {
		return nil
	}
	return &sp
}

// miotTypes — модель → urn по общему каталогу. Каталог большой: читаем его потоком по одной записи
// и оставляем только модели из want — весь каталог в памяти не собирается.
func miotTypes(ctx context.Context, c *http.Client, want map[string]bool) map[string]string {
	out := map[string]string{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://miot-spec.org/miot-spec-v2/instances?status=released", nil)
	resp, err := c.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	return miotTypesFrom(io.LimitReader(resp.Body, 32<<20), want)
}

func miotTypesFrom(r io.Reader, want map[string]bool) map[string]string {
	out := map[string]string{}
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return out
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return out
		}
		if key != "instances" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return out
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return out
		}
		best := map[string]int{}
		for dec.More() {
			var in struct {
				Model   string `json:"model"`
				Type    string `json:"type"`
				Version int    `json:"version"`
			}
			if dec.Decode(&in) != nil {
				return out
			}
			if want[in.Model] && in.Version >= best[in.Model] {
				best[in.Model], out[in.Model] = in.Version, in.Type
			}
		}
		return out
	}
	return out
}

func (x *Xiaomi) save(devs []XiaomiDevice) {
	sort.Slice(devs, func(i, j int) bool { return devs[i].Room+devs[i].Name < devs[j].Room+devs[j].Name })
	x.mu.Lock()
	x.devices, x.loaded = devs, true
	x.mu.Unlock()
	if b, err := json.Marshal(devs); err == nil {
		_ = os.WriteFile(filepath.Join(x.dataDir, "xiaomi-devices.json"), b, 0o600)
	}
}

func (x *Xiaomi) Devices() []XiaomiDevice {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.loaded {
		x.loaded = true
		if b, err := os.ReadFile(filepath.Join(x.dataDir, "xiaomi-devices.json")); err == nil {
			_ = json.Unmarshal(b, &x.devices)
		}
	}
	return x.devices
}

// ---------------------------------------------------------------- для модели

func (x *Xiaomi) context() string {
	if !x.ready() {
		return ""
	}
	devs := x.Devices()
	if len(devs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nУстройства Xiaomi Home (управление — инструмент xiaomi_home; передавай did и имя свойства/действия ровно как здесь, " +
		"значения — в формате и диапазоне свойства; если не уверен в состоянии — сначала action=get):")
	for _, d := range devs {
		state := "в сети"
		if !d.Online {
			state = "не в сети"
		}
		fmt.Fprintf(&b, "\n- did %s «%s» (%s, %s, %s)", d.DID, d.Name, orDash(d.Room), orDash(d.Type), state)
		var props []string
		for _, p := range d.Props {
			s := p.Name + " [" + p.Format
			if len(p.Range) >= 2 {
				s += fmt.Sprintf(" %v..%v", p.Range[0], p.Range[1])
			}
			if len(p.Values) > 0 && len(p.Values) <= 12 {
				s += " " + strings.Join(p.Values, ", ")
			}
			if p.Unit != "" && p.Unit != "none" {
				s += " " + p.Unit
			}
			s += ", " + p.Access + "]"
			props = append(props, s)
		}
		if len(props) > 0 {
			b.WriteString(": " + strings.Join(props, "; "))
		}
		var acts []string
		for _, a := range d.Actions {
			acts = append(acts, a.Name)
		}
		if len(acts) > 0 {
			b.WriteString("; действия: " + strings.Join(acts, ", "))
		}
	}
	return b.String()
}

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

func (x *Xiaomi) control(ctx context.Context, args string) string {
	var a struct {
		DID    string `json:"did"`
		Action string `json:"action"`
		Name   string `json:"name"`
		Value  any    `json:"value"`
		Params []any  `json:"params"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return "Не понял команду для Xiaomi."
	}
	var dev *XiaomiDevice
	devs := x.Devices()
	for i := range devs {
		if devs[i].DID == a.DID || strings.EqualFold(devs[i].Name, a.DID) {
			dev = &devs[i]
		}
	}
	if dev == nil {
		return "Такого устройства Xiaomi нет в списке."
	}
	region := dev.Region
	if region == "" {
		region, _, _ = strings.Cut(x.store.Get().Xiaomi.Region, ",")
	}
	switch a.Action {
	case "get":
		var params []map[string]any
		var names []string
		for _, p := range dev.Props {
			if strings.Contains(p.Access, "r") && (a.Name == "" || p.Name == a.Name) {
				params = append(params, map[string]any{"did": dev.DID, "siid": p.SIID, "piid": p.PIID})
				names = append(names, p.Name)
			}
		}
		if len(params) == 0 {
			return "У устройства нет такого свойства для чтения."
		}
		if len(params) > 30 {
			params, names = params[:30], names[:30]
		}
		var res []struct {
			SIID  int `json:"siid"`
			PIID  int `json:"piid"`
			Code  int `json:"code"`
			Value any `json:"value"`
		}
		if err := x.call(ctx, region, "/miotspec/prop/get", map[string]any{"params": params}, &res); err != nil {
			return "Xiaomi не ответил: " + shortErr(err)
		}
		var out []string
		for _, r := range res {
			for i, p := range params {
				if p["siid"] == r.SIID && p["piid"] == r.PIID && r.Code == 0 {
					out = append(out, fmt.Sprintf("%s = %v", names[i], r.Value))
				}
			}
		}
		if len(out) == 0 {
			return "Устройство не ответило (возможно, не в сети)."
		}
		return "«" + dev.Name + "»: " + strings.Join(out, "; ")
	case "set":
		for _, p := range dev.Props {
			if p.Name == a.Name && strings.Contains(p.Access, "w") {
				var res []struct {
					Code int `json:"code"`
				}
				err := x.call(ctx, region, "/miotspec/prop/set", map[string]any{"params": []map[string]any{{"did": dev.DID, "siid": p.SIID, "piid": p.PIID, "value": a.Value}}}, &res)
				if err != nil {
					return "Xiaomi не выполнил: " + shortErr(err)
				}
				if len(res) > 0 && res[0].Code != 0 && res[0].Code != 1 {
					return fmt.Sprintf("Устройство отказало (код %d) — возможно, не в сети или значение вне диапазона.", res[0].Code)
				}
				log.Printf("Xiaomi Home: «%s» %s = %v", dev.Name, p.Name, a.Value)
				return "Готово: «" + dev.Name + "» " + p.Name + " = " + fmt.Sprint(a.Value)
			}
		}
		return "У устройства нет такого изменяемого свойства."
	case "do":
		for _, act := range dev.Actions {
			if act.Name == a.Name {
				in := a.Params
				if in == nil {
					in = []any{}
				}
				var res struct {
					Code int `json:"code"`
				}
				err := x.call(ctx, region, "/miotspec/action", map[string]any{"params": map[string]any{"did": dev.DID, "siid": act.SIID, "aiid": act.AIID, "in": in}}, &res)
				if err != nil {
					return "Xiaomi не выполнил: " + shortErr(err)
				}
				if res.Code != 0 && res.Code != 1 {
					return fmt.Sprintf("Устройство отказало (код %d).", res.Code)
				}
				log.Printf("Xiaomi Home: «%s» %s", dev.Name, act.Name)
				return "Готово: «" + dev.Name + "» " + act.Name
			}
		}
		return "У устройства нет такого действия."
	}
	return "Не понял действие для Xiaomi."
}
