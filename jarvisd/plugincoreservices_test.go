package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderScopedConfigAndConcurrentUpdate(t *testing.T) {
	s := &Store{s: defaultSettings(), path: filepath.Join(t.TempDir(), "settings.json")}
	s.s.OpenAIKey = "unrelated-private-key"
	s.s.Tuya = TuyaAuth{Endpoint: "https://example.invalid", RefreshToken: "old"}
	f := coreServices(s, &Proxy{}, t.TempDir())
	v, err := f("smartlife", "config.get", nil)
	b, _ := json.Marshal(v)
	if err != nil || strings.Contains(string(b), "unrelated-private-key") || !strings.Contains(string(b), "old") {
		t.Fatal("scope", string(b), err)
	}
	if _, err = f("random-plugin", "config.get", nil); err == nil {
		t.Fatal("unknown plugin read credentials")
	}
	before := s.Get().Tuya
	change, _ := json.Marshal(map[string]any{"before": before, "after": TuyaAuth{Endpoint: "https://example.invalid", RefreshToken: "new"}})
	if _, err = f("smartlife", "config.set", change); err != nil {
		t.Fatal(err)
	}
	if _, err = f("smartlife", "config.set", change); err == nil {
		t.Fatal("stale credential update was accepted")
	}
	change, _ = json.Marshal(map[string]any{"before": s.Get().Tuya, "after": TuyaAuth{}})
	if _, err = f("smartlife", "config.set", change); err != nil {
		t.Fatal(err)
	}
	if s.Get().Tuya.SignedIn() || s.Get().OpenAIKey != "unrelated-private-key" {
		t.Fatal("logout did not clear only its own credentials")
	}
}

func TestProviderNetworkFailsClosed(t *testing.T) {
	s := &Store{s: defaultSettings()}
	p := &Proxy{link: "vless://configured", err: "provider unavailable"}
	f := coreServices(s, p, t.TempDir())
	v, err := f("xiaomi", "network", nil)
	b, _ := json.Marshal(v)
	if err != nil || !strings.Contains(string(b), "provider unavailable") {
		t.Fatal("VPN failure became direct", v, err)
	}
	v, err = f("homeassistant", "network", nil)
	b, _ = json.Marshal(v)
	if err != nil || string(b) != "{}" {
		t.Fatal("local Home Assistant used VPN", v, err)
	}
}

func TestProviderRefreshPreservesHomeSwitch(t *testing.T) {
	s := &Store{s: defaultSettings(), path: filepath.Join(t.TempDir(), "settings.json")}
	s.s.Xiaomi = XiaomiAuth{UserID: "test-user", SSecurity: "test-security"}
	s.s.HomeUse = map[string]bool{"xiaomi": false}
	f := coreServices(s, &Proxy{}, t.TempDir())
	a := s.Get().Xiaomi
	a.ServiceToken = "renewed-token"
	change, _ := json.Marshal(map[string]any{"before": s.Get().Xiaomi, "after": a})
	if _, err := f("xiaomi", "config.set", change); err != nil {
		t.Fatal(err)
	}
	if s.Get().UseHome("xiaomi") {
		t.Fatal("silent refresh reenabled a disabled channel")
	}
	before := s.Get().Xiaomi
	_ = s.Update(func(st *Settings) error { st.Xiaomi = XiaomiAuth{}; return nil })
	change, _ = json.Marshal(map[string]any{"before": XiaomiAuth{}, "after": before, "connected": true})
	if _, err := f("xiaomi", "config.set", change); err != nil {
		t.Fatal(err)
	}
	if !s.Get().UseHome("xiaomi") {
		t.Fatal("explicit new login did not enable its channel")
	}
}

func TestProviderLogoutWhilePluginStopped(t *testing.T) {
	dir := t.TempDir()
	s := &Store{s: defaultSettings(), path: filepath.Join(dir, "settings.json")}
	s.s.Tuya = TuyaAuth{Endpoint: "https://example.invalid", RefreshToken: "test"}
	s.s.Xiaomi = XiaomiAuth{UserID: "other-account"}
	path := filepath.Join(dir, "plugins", "home", "data", "tuya-devices.json")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(`[]`), 0600)
	if err := forgetHomeAccount(s, "smartlife"); err != nil {
		t.Fatal(err)
	}
	if s.Get().Tuya.SignedIn() || s.Get().Xiaomi.UserID != "other-account" {
		t.Fatal("logout scope")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("private device cache retained", err)
	}
}
