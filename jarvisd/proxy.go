package main

// Выход в интернет для облачных сервисов: напрямую или через VPN.
//
// Сам VPN с 0.9.69 — плагин (официальный «vpn» на ядре Xray или любой другой с "provides": ["vpn"]):
// ядро отдаёт ему ссылку из настроек, плагин поднимает туннель и SOCKS5 на 127.0.0.1 с паролем,
// а ядро пускает через этот SOCKS те подключения, у которых в панели стоит «Через VPN» (netroute.go).
// Алиса и музыка Яндекса ходят напрямую, как раньше. Протокол — docs/PLUGINS.md, раздел «VPN».

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"

	"golang.org/x/net/proxy"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VPNInfo — что плагин понял из ссылки; показывается в панели, чтобы было видно, что поняли.
type VPNInfo struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Network  string `json:"network"`
	Security string `json:"security"`
	SNI      string `json:"sni"`
	Flow     string `json:"flow"`
	Finger   string `json:"fp"`
	Path     string `json:"path"`
	Insecure bool   `json:"insecure"`
}

// vpnTunnel — туннель, который держит плагин: SOCKS5 на 127.0.0.1 с паролем.
type vpnTunnel struct {
	Socks string   `json:"socks"`
	User  string   `json:"user"`
	Pass  string   `json:"pass"`
	Info  *VPNInfo `json:"info"`
}

// key — один и тот же путь в сеть (пулы соединений по нему и меняются).
func (t *vpnTunnel) key() string {
	if t == nil {
		return ""
	}
	return t.Socks + "|" + t.User + "|" + t.Pass
}

// vpnProvider — плагин, который умеет VPN: link "" — выключить туннель.
type vpnProvider interface {
	VPN(ctx context.Context, link string) (*vpnTunnel, error)
}

const vpnMissing = "VPN теперь отдельный плагин: поставьте «VPN» на странице «Плагины»"

// vpnStartWait bounds plugin RPCs. A missing tunnel never selects a direct route.
var vpnStartWait = 5 * time.Second

// Proxy — общий выход в интернет для облачных сервисов. Без ссылки — напрямую.
type Proxy struct {
	mu   sync.RWMutex
	link string     // ссылка из настроек
	tun  *vpnTunnel // nil with a configured link means unavailable, never direct
	err  string

	cleanup   func()
	providers *PluginHost // CLI-only providers; daemon uses the shared pluginHost
	applyMu   sync.Mutex
	// provider — плагин VPN сейчас; nil и причина, если его нет; starting — вот-вот запустится, стоит подождать
	provider func() (prov vpnProvider, why string, starting bool)

	warmMu sync.Mutex
	warm   map[string]*warmConn // готовые TLS-соединения по адресу «хост:443»

	use func(svc string) bool // галочка «через VPN» у подключения (netroute.go)

	poolMu     sync.Mutex
	transports map[string]servicePool
}

type warmConn struct {
	c  net.Conn
	at time.Time
}

type freshKey struct{}

// FreshDial — контекст, с которым готовое соединение не берётся: повтор после неудачи на «остывшем».
func FreshDial(ctx context.Context) context.Context { return context.WithValue(ctx, freshKey{}, true) }

// Замер на колонке (13.11.2026): сервер OpenAI закрывает соединение, по которому ничего не прислали,
// где-то между 8 и 15 секундами. Поэтому готовое соединение меняем каждые 5–6 секунд и не берём старше 8.
const (
	warmRefresh = 5500 * time.Millisecond
	warmMaxAge  = 8 * time.Second
)

func (p *Proxy) dialTLS(ctx context.Context, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	raw, err := p.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := tls.Client(raw, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err := c.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return c, nil
}

// KeepWarm — держать наготове одно TLS-соединение с адресом, пока want() возвращает true.
// Разговор тогда открывается без ожидания туннеля и TLS: в среднем около 1,0 с вместо 1,2–2,3 с
// (само соединение занимает 0,15–1,2 с). Соединение, по которому ничего не отправлено, бесплатное.
func (p *Proxy) KeepWarm(addr string, want func() bool) {
	go func() {
		for {
			time.Sleep(500 * time.Millisecond)
			p.warmMu.Lock()
			w := p.warm[addr]
			p.warmMu.Unlock()
			if !want() {
				if w != nil {
					p.dropWarm(addr)
				}
				continue
			}
			if w != nil && time.Since(w.at) < warmRefresh {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			c, err := p.dialTLS(ctx, addr)
			cancel()
			if err != nil {
				time.Sleep(10 * time.Second) // нет сети или туннеля — не долбим
				continue
			}
			p.warmMu.Lock() // новое готово — старое закрываем
			if p.warm == nil {
				p.warm = map[string]*warmConn{}
			}
			if old := p.warm[addr]; old != nil {
				old.c.Close()
			}
			p.warm[addr] = &warmConn{c: c, at: time.Now()}
			p.warmMu.Unlock()
		}
	}()
}

func (p *Proxy) dropWarm(addr string) {
	p.warmMu.Lock()
	if w := p.warm[addr]; w != nil {
		w.c.Close()
		delete(p.warm, addr)
	}
	p.warmMu.Unlock()
}

func (p *Proxy) takeWarm(addr string) net.Conn {
	p.warmMu.Lock()
	defer p.warmMu.Unlock()
	w := p.warm[addr]
	if w == nil {
		return nil
	}
	delete(p.warm, addr)
	if time.Since(w.at) > warmMaxAge {
		w.c.Close()
		return nil
	}
	log.Printf("сеть: беру готовое соединение с %s (ждало %.0f с)", addr, time.Since(w.at).Seconds())
	return w.c
}

// LiveClient — HTTP-клиент для открытия разговора (WebSocket): берёт готовое TLS-соединение, если оно есть.
func (p *Proxy) LiveClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: p.DialContext,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if ctx.Value(freshKey{}) == nil {
				if c := p.takeWarm(addr); c != nil {
					return c, nil
				}
			}
			return p.dialTLS(ctx, addr)
		},
		MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second,
	}}
}

// Apply records the selected route even when the plugin is unavailable: selected
// VPN traffic must never fall back to a direct connection after a plugin failure.
func (p *Proxy) Apply(link string) error {
	link = strings.TrimSpace(link)
	if link != "" && !strings.HasPrefix(strings.ToLower(link), "vless://") {
		return errors.New("ссылка должна начинаться с vless://")
	}
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	p.mu.Lock()
	p.link = link
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), vpnStartWait)
	defer cancel()
	return p.refreshLocked(ctx)
}

func (p *Proxy) refreshLocked(ctx context.Context) error {
	p.mu.RLock()
	link := p.link
	p.mu.RUnlock()
	var tun *vpnTunnel
	var err error
	if p.provider != nil {
		prov, why, _ := p.provider()
		if prov != nil {
			tun, err = prov.VPN(ctx, link)
		} else if link != "" {
			err = errors.New(why)
		}
	} else if link != "" {
		err = errors.New(vpnMissing)
	}
	if link != "" && err == nil {
		err = validVPNTunnel(tun)
	}
	if link == "" {
		tun = nil
		err = nil
	}
	if err != nil {
		tun = nil
	}
	p.mu.Lock()
	oldKey := p.tun.key()
	p.tun = tun
	p.err = ""
	if err != nil {
		p.err = err.Error()
	}
	p.mu.Unlock()
	if oldKey != tun.key() {
		p.warmMu.Lock()
		for addr, w := range p.warm {
			w.c.Close()
			delete(p.warm, addr)
		}
		p.warmMu.Unlock()
	}
	return err
}

func validVPNTunnel(t *vpnTunnel) error {
	if t == nil {
		return errors.New("плагин VPN не вернул туннель")
	}
	host, port, err := net.SplitHostPort(t.Socks)
	ip := net.ParseIP(host)
	n, _ := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || n < 1 || n > 65535 || len(t.User) == 0 || len(t.User) > 255 || len(t.Pass) == 0 || len(t.Pass) > 255 {
		return errors.New("плагин VPN вернул неправильный локальный SOCKS")
	}
	return nil
}

func (p *Proxy) Run(ctx context.Context) {
	for {
		p.applyMu.Lock()
		c, cancel := context.WithTimeout(ctx, vpnStartWait)
		_ = p.refreshLocked(c)
		cancel()
		p.applyMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (p *Proxy) Status() (*VPNInfo, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.tun != nil && p.tun.Info != nil {
		v := *p.tun.Info
		return &v, p.err
	}
	return nil, p.err
}

func (p *Proxy) route(svc string) (string, *vpnTunnel, string) {
	use := p.use == nil || svc == "" || p.use(svc)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !use || p.link == "" {
		return "direct", nil, ""
	}
	if p.tun != nil {
		return p.tun.key(), p.tun, ""
	}
	why := p.err
	if why == "" {
		why = "плагин VPN запускается"
	}
	return "blocked|" + why, nil, why
}

func dialTunnel(ctx context.Context, t *vpnTunnel, network, addr string) (net.Conn, error) {
	d, err := proxy.SOCKS5("tcp", t.Socks, &proxy.Auth{User: t.User, Password: t.Pass}, directDialer)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS не поддерживает контекст")
	}
	return cd.DialContext(ctx, network, addr)
}

func (p *Proxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	_, t, why := p.route("")
	if why != "" {
		return nil, errors.New(why)
	}
	if t != nil {
		return dialTunnel(ctx, t, network, addr)
	}
	return directDialer.DialContext(ctx, network, addr)
}

func (p *Proxy) Client(timeout time.Duration) *http.Client { return p.ClientFor("", timeout) }

var DirectClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
	DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxIdleConns: 4, IdleConnTimeout: 120 * time.Second,
}}

func (n *nativePlugin) VPN(ctx context.Context, link string) (*vpnTunnel, error) {
	raw, err := n.request(ctx, "vpn", map[string]any{"link": link})
	if err != nil {
		return nil, err
	}
	var t vpnTunnel
	if err = json.Unmarshal(raw, &t); err != nil {
		return nil, errors.New("плагин VPN вернул не JSON")
	}
	return &t, nil
}

// VPNProvider selects one enabled native provider; the official id wins ties.
func (h *PluginHost) VPNProvider() (vpnProvider, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var chosen *Plugin
	disabled := false
	for _, p := range h.plugins {
		found := false
		for _, cap := range p.Manifest.Provides {
			if cap == "vpn" {
				found = true
			}
		}
		st := h.state[p.Manifest.ID]
		if found && st != nil && !st.Enabled && !st.Removed {
			disabled = true
		}
		if !found || st == nil || !st.Enabled || st.Removed {
			continue
		}
		if chosen == nil || p.Manifest.ID == "vpn" || (chosen.Manifest.ID != "vpn" && p.Manifest.ID < chosen.Manifest.ID) {
			chosen = p
		}
	}
	if chosen == nil {
		if disabled {
			return nil, "Плагин VPN выключен — включите его на странице «Плагины»", false
		}
		return nil, vpnMissing, false
	}
	if n, ok := chosen.rt.(*nativePlugin); ok {
		return n, "", false
	}
	why := chosen.err
	if why == "" {
		why = "плагин VPN запускается"
	}
	return nil, why, true
}

// CLI probes load only the network provider, without starting unrelated plugins.
func probeProxy(store *Store, capabilities ...string) *Proxy {
	p := &Proxy{}
	h := NewPluginHost(filepath.Dir(store.path), PluginHooks{}, func() *http.Client { return DirectClient })
	h.hooks.Request = coreServices(store, p, filepath.Dir(store.path))
	h.hooks.Info = func() map[string]any { return map[string]any{"probe": true} }
	p.providers = h
	var state map[string]*pluginState
	if b, err := os.ReadFile(h.statePath()); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	for id, st := range state {
		if st != nil {
			h.state[id] = st
		}
	}
	dirs, _ := os.ReadDir(h.dir())
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(h.dir(), d.Name())
		m, err := readManifest(dir)
		if err != nil || m.ID != d.Name() {
			continue
		}
		found := false
		for _, cap := range m.Provides {
			if cap == "vpn" {
				found = true
			}
			for _, needed := range capabilities {
				if cap == needed {
					found = true
				}
			}
		}
		if !found {
			continue
		}
		st := h.stateLocked(m.ID)
		if !st.Enabled || st.Removed {
			continue
		}
		plugin := &Plugin{Manifest: m, Dir: dir}
		h.plugins[m.ID] = plugin
		h.start(plugin)
	}
	p.provider = h.VPNProvider
	p.cleanup = func() {
		for _, plugin := range h.plugins {
			h.stop(plugin)
		}
	}
	return p
}

func (p *Proxy) Close() {
	if p.cleanup != nil {
		p.cleanup()
	}
}
