// Плагин «VPN» для YaJarvis: туннель VLESS на ядре Xray.
//
// Ядро присылает ссылку (метод "vpn"), плагин поднимает туннель и SOCKS5 на 127.0.0.1 с одноразовым паролем,
// а ядро пускает через него те подключения, у которых в панели стоит «Через VPN». Алиса и всё остальное
// на колонке ходят напрямую. Протокол — docs/PLUGINS.md, раздел «VPN».
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	xlog "github.com/xtls/xray-core/app/log"
	clog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"

	"github.com/newfpv/YaJavis/pluginsdk"
)

func init() {
	// stdout занят протоколом с ядром: журнал Xray — в stderr, ядро перепишет его в свой журнал
	_ = xlog.RegisterHandlerCreator(xlog.LogType_Console, func(xlog.LogType, xlog.HandlerCreatorOptions) (clog.Handler, error) {
		return clog.NewLogger(clog.CreateStderrLogWriter()), nil
	})
}

type tunnel struct {
	sdk *pluginsdk.Plugin

	mu    sync.Mutex // apply по одному
	cur   sync.RWMutex
	inst  *core.Instance
	link  *VlessLink
	raw   string
	socks *socksServer
}

// vpnState — ответ ядру: куда ходить и что поняли из ссылки (показывается в панели).
type vpnState struct {
	Socks string     `json:"socks,omitempty"`
	User  string     `json:"user,omitempty"`
	Pass  string     `json:"pass,omitempty"`
	Info  *VlessLink `json:"info,omitempty"`
}

// noFlow — ссылки, у которых сервер не принимает flow из ссылки (частая ошибка панелей x-ui:
// в подписке flow=xtls-rprx-vision, а у самого клиента Flow пустой — сервер молча рвёт связь, EOF).
var noFlow sync.Map

func main() {
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(50) // колонке память дороже процессора
	}
	p := pluginsdk.New()
	t := &tunnel{sdk: p}
	p.Handle("vpn", func(params json.RawMessage) (any, error) {
		var in struct {
			Link string `json:"link"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		return t.apply(strings.TrimSpace(in.Link))
	})
	p.Run()
	t.close()
}

func (t *tunnel) apply(link string) (*vpnState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if link == "" {
		t.swap(nil, nil, "")
		t.sdk.Status("Выключен: ссылки нет")
		return &vpnState{}, nil
	}
	if link == t.raw && t.inst != nil { // ядро переспросило — туннель уже стоит
		return t.state(), nil
	}
	_, skipFlow := noFlow.Load(link)
	v, inst, err := build(link, skipFlow)
	if err != nil {
		t.swap(nil, nil, "")
		t.sdk.Status("Ошибка: " + err.Error())
		return nil, err
	}
	if t.socks == nil {
		s, err := startSocks(t.dial)
		if err != nil {
			inst.Close()
			return nil, fmt.Errorf("не открыть локальный порт: %w", err)
		}
		t.socks = s
	}
	t.swap(inst, v, link)
	t.sdk.Status(describe(v))
	if v.Flow != "" && !skipFlow {
		go t.checkFlow(link, v)
	}
	return t.state(), nil
}

func describe(v *VlessLink) string {
	return fmt.Sprintf("Туннель до %s:%d (%s + %s)", v.Host, v.Port, v.Network, v.Security)
}

func build(link string, skipFlow bool) (*VlessLink, *core.Instance, error) {
	v, err := ParseVless(link)
	if err != nil {
		return nil, nil, err
	}
	if skipFlow {
		v.Flow = ""
	}
	cfg, err := v.coreConfig()
	if err != nil {
		return nil, nil, errors.New("Xray не принял настройки: " + err.Error())
	}
	inst, err := core.New(cfg)
	if err == nil {
		err = inst.Start()
	}
	if err != nil {
		if inst != nil {
			inst.Close()
		}
		return nil, nil, errors.New("Xray не запустился: " + err.Error())
	}
	return v, inst, nil
}

// swap — поставить новый туннель; соединения старого закрываются вместе с ним.
func (t *tunnel) swap(inst *core.Instance, v *VlessLink, raw string) {
	t.cur.Lock()
	old := t.inst
	t.inst, t.link, t.raw = inst, v, raw
	t.cur.Unlock()
	if old != nil {
		old.Close()
	}
}

func (t *tunnel) state() *vpnState {
	t.cur.RLock()
	defer t.cur.RUnlock()
	if t.inst == nil || t.socks == nil {
		return &vpnState{}
	}
	return &vpnState{Socks: t.socks.Addr(), User: t.socks.user, Pass: t.socks.pass, Info: t.link}
}

func (t *tunnel) dial(ctx context.Context, host string, port uint16) (net.Conn, error) {
	t.cur.RLock()
	inst := t.inst
	t.cur.RUnlock()
	return dialVia(ctx, inst, host, port)
}

func dialVia(ctx context.Context, inst *core.Instance, host string, port uint16) (net.Conn, error) {
	if inst == nil {
		return nil, errors.New("туннель выключен")
	}
	return core.Dial(ctx, inst, xnet.TCPDestination(xnet.ParseAddress(host), xnet.Port(port)))
}

// checkFlow — если через туннель ничего не открывается, а без flow открывается, работаем без flow.
func (t *tunnel) checkFlow(link string, v *VlessLink) {
	t.cur.RLock()
	inst := t.inst
	t.cur.RUnlock()
	if err := probe(inst); err == nil || !strings.Contains(err.Error(), "EOF") {
		return
	}
	_, alt, err := build(link, true)
	if err != nil {
		return
	}
	if probe(alt) != nil {
		alt.Close() // без flow тоже не работает — дело не во flow
		return
	}
	alt.Close()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.raw != link { // ссылку за это время поменяли
		return
	}
	noFlow.Store(link, true)
	nv, inst2, err := build(link, true)
	if err != nil {
		return
	}
	t.swap(inst2, nv, link)
	t.sdk.Log("сервер не принимает flow=%s из ссылки (у клиента в x-ui Flow пустой) — работаю без flow", v.Flow)
	t.sdk.Status(describe(nv) + ", без flow")
	t.sdk.Notify("vpn", t.state())
}

func probe(inst *core.Instance) error {
	c := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			p, err := xnet.PortFromString(port)
			if err != nil {
				return nil, err
			}
			return dialVia(context.WithoutCancel(ctx), inst, host, uint16(p))
		},
	}}
	defer c.CloseIdleConnections()
	r, err := c.Get("https://www.google.com/generate_204")
	if err == nil {
		r.Body.Close()
	}
	return err
}

func (t *tunnel) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.swap(nil, nil, "")
	if t.socks != nil {
		t.socks.Close()
	}
}
