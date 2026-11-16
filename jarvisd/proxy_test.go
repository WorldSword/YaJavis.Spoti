package main

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Реальный туннель: JARVIS_VLESS=vless://… go test -run Live -v
func TestVlessLive(t *testing.T) {
	link := os.Getenv("JARVIS_VLESS")
	if link == "" {
		t.Skip("нет JARVIS_VLESS")
	}
	st, err := OpenStore("/data/opt/jarvisd/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	p := probeProxy(st)
	defer p.Close()
	if err := p.Apply(link); err != nil {
		t.Fatal(err)
	}
	c := p.Client(15 * time.Second)
	for _, u := range []string{"https://api.openai.com/v1/models", "https://api.elevenlabs.io/v2/voices?page_size=1", "https://ifconfig.me/ip"} {
		t0 := time.Now()
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		if u == "https://ifconfig.me/ip" {
			t.Logf("%s → %d за %v, выход через %s", u, resp.StatusCode, time.Since(t0).Round(time.Millisecond), b)
		} else {
			t.Logf("%s → %d за %v", u, resp.StatusCode, time.Since(t0).Round(time.Millisecond))
		}
		if resp.StatusCode != 401 && resp.StatusCode != 200 {
			t.Errorf("%s: неожиданный ответ %d", u, resp.StatusCode)
		}
	}
	_ = http.StatusOK
}
