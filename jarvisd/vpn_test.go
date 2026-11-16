package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type testVPNProvider struct {
	tun *vpnTunnel
	err error
}

func (f *testVPNProvider) VPN(context.Context, string) (*vpnTunnel, error) { return f.tun, f.err }

func TestVPNMissingNeverFallsBack(t *testing.T) {
	p := &Proxy{}
	if err := p.Apply("vless://test"); err == nil {
		t.Fatal("missing plugin accepted")
	}
	if _, err := p.ClientFor("openai", time.Second).Get("http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "плагин") {
		t.Fatalf("missing provider did not block traffic: %v", err)
	}
	if err := p.Apply(""); err != nil {
		t.Fatal(err)
	}
	if route, _, why := p.route("openai"); route != "direct" || why != "" {
		t.Fatal("clearing link did not restore direct mode")
	}
}

func TestVPNSocksAndProviderFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		var h [2]byte
		if _, err = io.ReadFull(c, h[:]); err != nil {
			return
		}
		methods := make([]byte, int(h[1]))
		if _, err = io.ReadFull(c, methods); err != nil {
			return
		}
		_, _ = c.Write([]byte{5, 2})
		if _, err = io.ReadFull(c, h[:]); err != nil {
			return
		}
		u := make([]byte, int(h[1]))
		_, _ = io.ReadFull(c, u)
		var n [1]byte
		_, _ = io.ReadFull(c, n[:])
		pw := make([]byte, int(n[0]))
		_, _ = io.ReadFull(c, pw)
		if string(u) != "once" || string(pw) != "secret" {
			return
		}
		_, _ = c.Write([]byte{1, 0})
		var req [5]byte
		if _, err = io.ReadFull(c, req[:]); err != nil {
			return
		}
		if req[3] != 3 {
			return
		}
		host := make([]byte, int(req[4]))
		_, _ = io.ReadFull(c, host)
		var port [2]byte
		_, _ = io.ReadFull(c, port[:])
		got <- string(host) + ":" + itoa(int(binary.BigEndian.Uint16(port[:])))
		_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		buf := make([]byte, 2048)
		_, _ = c.Read(buf)
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	}()
	f := &testVPNProvider{tun: &vpnTunnel{Socks: ln.Addr().String(), User: "once", Pass: "secret", Info: &VPNInfo{Name: "test"}}}
	p := &Proxy{provider: func() (vpnProvider, string, bool) { return f, "", false }}
	if err = p.Apply("vless://test"); err != nil {
		t.Fatal(err)
	}
	client := p.ClientFor("openai", 3*time.Second)
	res, err := client.Get("http://remote.invalid:1234/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "ok" {
		t.Fatal(string(b))
	}
	select {
	case host := <-got:
		if host != "remote.invalid:1234" {
			t.Fatal(host)
		}
	case <-time.After(time.Second):
		t.Fatal("destination not proxied")
	}
	f.err = errors.New("plugin stopped")
	if err = p.Apply("vless://test"); err == nil {
		t.Fatal("failed plugin accepted")
	}
	if _, err = client.Get("http://127.0.0.1:1/"); err == nil || !strings.Contains(err.Error(), "plugin stopped") {
		t.Fatalf("cached client bypassed failed VPN: %v", err)
	}
	p.use = func(string) bool { return false }
	if route, _, why := p.route("youtube"); route != "direct" || why != "" {
		t.Fatal("explicit direct route blocked")
	}
}

func TestVPNRejectsNonLocalOrUnauthenticatedSocks(t *testing.T) {
	for _, v := range []*vpnTunnel{nil, {Socks: "example.com:123", User: "u", Pass: "p"}, {Socks: "192.0.2.1:123", User: "u", Pass: "p"}, {Socks: "127.0.0.1:123"}} {
		if validVPNTunnel(v) == nil {
			t.Fatalf("accepted unsafe endpoint: %+v", v)
		}
	}
}
