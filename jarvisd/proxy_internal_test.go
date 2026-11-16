package main

import (
	"io"
	"os"
	"testing"
	"time"
)

// Туннель до сервера, который пускает только к своим портам: JARVIS_VLESS=… JARVIS_VLESS_URL=http://127.0.0.1:18080/
func TestVlessInternal(t *testing.T) {
	link, u := os.Getenv("JARVIS_VLESS"), os.Getenv("JARVIS_VLESS_URL")
	if link == "" || u == "" {
		t.Skip("нет JARVIS_VLESS / JARVIS_VLESS_URL")
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
	t0 := time.Now()
	resp, err := p.Client(10 * time.Second).Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 120))
	resp.Body.Close()
	t.Logf("%s → %d за %v: %q", u, resp.StatusCode, time.Since(t0).Round(time.Millisecond), b)
}
