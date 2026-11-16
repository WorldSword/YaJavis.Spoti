package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestParseVless(t *testing.T) {
	ok := []string{
		"vless://3b1f7c2e-1d2a-4b8e-9c3f-0a1b2c3d4e5f@example.com:443?type=tcp&security=reality&pbk=Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw&sid=6ba85179e30d4fc2&sni=www.microsoft.com&fp=chrome&flow=xtls-rprx-vision#Мой",
		"vless://3b1f7c2e-1d2a-4b8e-9c3f-0a1b2c3d4e5f@1.2.3.4:8443?type=ws&security=tls&path=%2Fws&host=cdn.example.com&sni=cdn.example.com",
	}
	for _, l := range ok {
		v, err := ParseVless(l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if _, err := v.coreConfig(); err != nil {
			t.Fatalf("config: %v", err)
		}
	}
	bad := []string{"vless://3b1f7c2e-1d2a-4b8e-9c3f-0a1b2c3d4e5f@1.2.3.4:443?type=grpc&security=reality&serviceName=grpc&pbk=Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw&sid=ab&sni=a.com", "vmess://x", "vless://@h:1", "vless://3b1f7c2e-1d2a-4b8e-9c3f-0a1b2c3d4e5f@h?security=reality", "vless://3b1f7c2e-1d2a-4b8e-9c3f-0a1b2c3d4e5f@h:443?security=reality&pbk=short"}
	for _, l := range bad {
		if _, err := ParseVless(l); err == nil {
			t.Fatalf("должна быть ошибка: %s", l)
		}
	}
}

// SOCKS5: пускает только со своим паролем и доводит запрос до адреса назначения.
func TestSocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	s, err := startSocks(func(ctx context.Context, host string, port uint16) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, itoa(port)))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.ln.Close()
	get := func(user, pass string) (string, error) {
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return socksDialForTest(ctx, s.Addr(), user, pass, addr)
		}}}
		r, err := c.Get(srv.URL)
		if err != nil {
			return "", err
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return string(b), nil
	}
	if b, err := get(s.user, s.pass); err != nil || b != "ok" {
		t.Fatalf("свой пароль: %q %v", b, err)
	}
	if _, err := get(s.user, "чужой"); err == nil {
		t.Fatal("чужой пароль пропущен")
	}
}

// Настоящий туннель: JARVIS_VLESS=vless://… go test -run Live -v
func TestVlessLive(t *testing.T) {
	link := os.Getenv("JARVIS_VLESS")
	if link == "" {
		t.Skip("нет JARVIS_VLESS")
	}
	_, inst, err := build(link, false)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := probe(inst); err != nil {
		t.Fatal(err)
	}
}
