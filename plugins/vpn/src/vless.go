package main

// Ссылка vless://… → конфиг ядра Xray. Перенесено из jarvisd (до 0.9.69 VPN был встроен в ядро).

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/app/dispatcher"
	xlog "github.com/xtls/xray-core/app/log"
	"github.com/xtls/xray-core/app/proxyman"
	clog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/tcp"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
	"github.com/xtls/xray-core/transport/internet/websocket"

	// регистрация нужных частей ядра (только клиент VLESS — меньше программа)
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/websocket"
)

// VlessLink — разобранная ссылка; поля показываются в веб-настройках, чтобы было видно, что поняли.
type VlessLink struct {
	Name     string `json:"name"`
	UUID     string `json:"-"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Network  string `json:"network"`  // tcp, ws, grpc, xhttp
	Security string `json:"security"` // none, tls, reality
	SNI      string `json:"sni"`
	Flow     string `json:"flow"`
	Finger   string `json:"fp"`
	PubKey   string `json:"-"`
	ShortID  string `json:"-"`
	SpiderX  string `json:"-"`
	Path     string `json:"path"`
	HostHdr  string `json:"-"`
	Service  string `json:"-"`
	ALPN     string `json:"-"`
	Mode     string `json:"-"`
	Insecure bool   `json:"insecure"` // allowInsecure=1: сертификат сервера не проверяется
}

func ParseVless(link string) (*VlessLink, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(strings.ToLower(link), "vless://") {
		return nil, errors.New("ссылка должна начинаться с vless://")
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("не разобрать ссылку: %v", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, errors.New("в ссылке нет UUID (часть до @)")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("в ссылке нет порта сервера")
	}
	q := u.Query()
	get := func(k, def string) string {
		if v := q.Get(k); v != "" {
			return v
		}
		return def
	}
	v := &VlessLink{
		Name: u.Fragment, UUID: u.User.Username(), Host: u.Hostname(), Port: port,
		Network: strings.ToLower(get("type", "tcp")), Security: strings.ToLower(get("security", "none")),
		SNI: get("sni", get("peer", "")), Flow: q.Get("flow"), Finger: get("fp", "chrome"),
		PubKey: get("pbk", ""), ShortID: q.Get("sid"), SpiderX: q.Get("spx"),
		Path: q.Get("path"), HostHdr: q.Get("host"), Service: q.Get("serviceName"), ALPN: q.Get("alpn"), Mode: q.Get("mode"),
		Insecure: q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" || q.Get("allowInsecure") == "true",
	}
	if v.Network == "raw" {
		v.Network = "tcp"
	}
	switch v.Network {
	case "tcp", "ws":
	case "grpc":
		// gRPC убран: он один добавлял 2,4 МБ к программе, а на флешке колонки всего 87 МБ
		return nil, errors.New("транспорт gRPC не поддерживается — возьмите ссылку с type=tcp (REALITY) или ws")
	default:
		return nil, fmt.Errorf("транспорт %q пока не поддерживается (есть tcp, ws)", v.Network)
	}
	if e := q.Get("encryption"); e != "" && e != "none" {
		return nil, errors.New("постквантовое шифрование VLESS (encryption) пока не поддерживается")
	}
	if _, err := uuid.ParseString(v.UUID); err != nil {
		return nil, errors.New("UUID в ссылке неправильный")
	}
	if v.Flow != "" && v.Flow != vless.XRV && v.Flow != vless.XRV+"-udp443" {
		return nil, fmt.Errorf("flow %q не поддерживается", v.Flow)
	}
	if v.Security == "reality" {
		if k, err := base64.RawURLEncoding.DecodeString(v.PubKey); err != nil || len(k) != 32 {
			return nil, errors.New("pbk (ключ REALITY) в ссылке неправильный")
		}
		if len(v.ShortID) > 16 || len(v.ShortID)%2 != 0 {
			return nil, errors.New("sid (short id REALITY) в ссылке неправильный")
		}
		if _, err := hex.DecodeString(v.ShortID); err != nil {
			return nil, errors.New("sid (short id REALITY) в ссылке неправильный")
		}
		if v.Network == "ws" {
			return nil, errors.New("REALITY не работает с ws — проверьте ссылку")
		}
	}
	switch v.Security {
	case "none", "tls", "reality":
	default:
		return nil, fmt.Errorf("шифрование %q не поддерживается (есть tls, reality)", v.Security)
	}
	if v.Security == "reality" && v.PubKey == "" {
		return nil, errors.New("для REALITY в ссылке нужен pbk (публичный ключ)")
	}
	return v, nil
}

func (v *VlessLink) coreConfig() (*core.Config, error) {
	sni := v.SNI
	if sni == "" && net.ParseIP(v.Host) == nil {
		sni = v.Host
	}
	fp := strings.ToLower(v.Finger)
	if fp == "" {
		fp = "chrome"
	}
	if xtls.GetFingerprint(fp) == nil {
		fp = "chrome"
	}
	stream := &internet.StreamConfig{ProtocolName: map[string]string{"tcp": "tcp", "ws": "websocket"}[v.Network]}
	switch v.Network {
	case "tcp":
		stream.TransportSettings = append(stream.TransportSettings, &internet.TransportConfig{ProtocolName: "tcp", Settings: serial.ToTypedMessage(&tcp.Config{})})
	case "ws":
		path := v.Path
		if path == "" {
			path = "/"
		}
		stream.TransportSettings = append(stream.TransportSettings, &internet.TransportConfig{ProtocolName: "websocket",
			Settings: serial.ToTypedMessage(&websocket.Config{Path: path, Host: v.HostHdr})})
	}
	switch v.Security {
	case "tls":
		t := &xtls.Config{ServerName: sni, Fingerprint: fp, AllowInsecure: v.Insecure}
		if v.ALPN != "" {
			t.NextProtocol = strings.Split(v.ALPN, ",")
		}
		tm := serial.ToTypedMessage(t)
		stream.SecuritySettings, stream.SecurityType = []*serial.TypedMessage{tm}, tm.Type
	case "reality":
		pk, _ := base64.RawURLEncoding.DecodeString(v.PubKey)
		sid := make([]byte, 8)
		_, _ = hex.Decode(sid, []byte(v.ShortID))
		spx := v.SpiderX
		if spx == "" || spx[0] != '/' {
			spx = "/"
		}
		r := &reality.Config{ServerName: sni, Fingerprint: fp, PublicKey: pk, ShortId: sid, SpiderX: spx, SpiderY: make([]int64, 10)}
		tm := serial.ToTypedMessage(r)
		stream.SecuritySettings, stream.SecurityType = []*serial.TypedMessage{tm}, tm.Type
	}
	id, _ := uuid.ParseString(v.UUID)
	account := &vless.Account{Id: id.String(), Flow: v.Flow, Encryption: "none"}
	out := &core.OutboundHandlerConfig{
		Tag:            "proxy",
		SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{StreamSettings: stream}),
		ProxySettings: serial.ToTypedMessage(&vlessout.Config{Vnext: &protocol.ServerEndpoint{
			Address: xnet.NewIPOrDomain(xnet.ParseAddress(v.Host)), Port: uint32(v.Port),
			User: &protocol.User{Account: serial.ToTypedMessage(account)}}}),
	}
	return &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&xlog.Config{ErrorLogType: xlog.LogType_Console, ErrorLogLevel: clog.Severity_Warning, AccessLogType: xlog.LogType_None}),
			serial.ToTypedMessage(&dispatcher.Config{}), serial.ToTypedMessage(&proxyman.OutboundConfig{})},
		Outbound: []*core.OutboundHandlerConfig{out},
	}, nil
}
