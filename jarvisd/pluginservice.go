package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Provider resolves the running process on every call, so toggles and restarts
// take effect without retaining a dead runtime or falling back to built-in code.
func (h *PluginHost) Provider(capability string) (*nativePlugin, error) {
	name := capability
	switch capability {
	case "yandexhome", "homeassistant", "xiaomi", "smartlife":
		name = "«Умный дом»"
	}
	if h == nil {
		return nil, fmt.Errorf("плагин %s не установлен", name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.plugins))
	for id := range h.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i, id := range ids {
		if id == capability {
			ids = append([]string{id}, append(ids[:i], ids[i+1:]...)...)
			break
		}
	}
	var disabled, starting bool
	for _, id := range ids {
		p := h.plugins[id]
		found := false
		for _, c := range p.Manifest.Provides {
			if c == capability {
				found = true
			}
		}
		if !found {
			continue
		}
		st := h.state[id]
		if st == nil || st.Removed {
			continue
		}
		if !st.Enabled {
			disabled = true
			continue
		}
		if n, ok := p.rt.(*nativePlugin); ok {
			return n, nil
		}
		starting = true
	}
	if starting {
		return nil, fmt.Errorf("плагин %s запускается", name)
	}
	if disabled {
		return nil, fmt.Errorf("плагин %s выключен — включите его на странице «Плагины»", name)
	}
	return nil, fmt.Errorf("установите плагин %s на странице «Плагины»", name)
}

func homeProviderState() map[string]any {
	_, err := pluginHost.Provider("homeassistant")
	if err != nil {
		return map[string]any{"available": false, "error": err.Error()}
	}
	return map[string]any{"available": true}
}

func serviceCall(ctx context.Context, capability, method string, params, out any) error {
	return serviceCallHost(ctx, pluginHost, capability, method, params, out)
}

func serviceCallHost(ctx context.Context, host *PluginHost, capability, method string, params, out any) error {
	n, err := host.Provider(capability)
	if err != nil {
		return err
	}
	raw, err := n.request(ctx, method, params)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
