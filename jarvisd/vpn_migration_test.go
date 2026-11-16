package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginsEnsureVPNBeforeCoreUpgrade(t *testing.T) {
	manifest := `{"id":"vpn","name":"VPN","version":"1.0.0","kind":"native","main":"vpn-{arch}","category":"system","provides":["vpn"],"min_core":"0.9.69"}`
	raw := makeZip(t, map[string]string{"plugin.json": manifest, "vpn-" + pluginArch(): "future executable"})
	fakeGitHub(t, map[string]string{"v0.9.69": `{"plugins":[{"id":"vpn","name":"VPN","category":"system","auto":{"settings":["vless"]}}]}`}, map[string][]byte{"plugin-vpn-" + pluginArch() + ".zip": raw})
	oldVer, oldSettings := version, pluginSettingsMap
	version = "0.9.68"
	defer func() { version = oldVer; pluginSettingsMap = oldSettings }()
	pluginSettingsMap = func() map[string]any { return map[string]any{"vless": "vless://configured"} }
	h := testHost(t)
	done, err := h.EnsureFor(context.Background(), "v0.9.69", true)
	if err != nil || strings.Join(done, ",") != "vpn" {
		t.Fatalf("migration: %v %v", done, err)
	}
	if _, err = os.Stat(filepath.Join(h.dir(), "vpn", "vpn-"+pluginArch())); err != nil {
		t.Fatal(err)
	}
	list := h.List()
	if len(list) != 1 || list[0].Running || !strings.Contains(list[0].Error, "после обновления") {
		t.Fatalf("future VPN executed on old core: %+v", list)
	}
	if err = h.Uninstall("vpn"); err != nil {
		t.Fatal(err)
	}
	if done, err = h.EnsureFor(context.Background(), "v0.9.69", true); err != nil || len(done) != 0 {
		t.Fatal("removed plugin came back", done, err)
	}
	h2 := testHost(t)
	pluginSettingsMap = func() map[string]any { return map[string]any{"vless": ""} }
	if done, err = h2.EnsureFor(context.Background(), "v0.9.69", true); err != nil || len(done) != 0 {
		t.Fatal("VPN installed without configuration", done, err)
	}
}
