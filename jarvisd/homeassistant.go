package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HARoomBinding struct {
	Aliases      []string `json:"aliases,omitempty"`
	InfoEntity   string   `json:"info_entity,omitempty"`
	InfoRoom     string   `json:"info_room,omitempty"`
	ResultEntity string   `json:"result_entity,omitempty"`
	Name         string   `json:"name"`
	Vacuum       string   `json:"vacuum,omitempty"`
	Entity       string   `json:"entity"`
	RoomIDs      string   `json:"room_ids,omitempty"`
}
type HASettings struct {
	URL   string          `json:"url"`
	Token string          `json:"token"`
	Rooms []HARoomBinding `json:"rooms,omitempty"`
}
type HAEntity struct {
	LastUpdated  time.Time      `json:"last_updated"`
	LastReported time.Time      `json:"last_reported"`
	ID           string         `json:"entity_id"`
	State        string         `json:"state"`
	Attributes   map[string]any `json:"attributes"`
}

func (e HAEntity) name() string {
	if s, ok := e.Attributes["friendly_name"].(string); ok {
		return s
	}
	return e.ID
}

// HomeAssistant is a panel/engine adapter. All device logic lives in the plugin.
type HomeAssistant struct {
	config func() HASettings
	client *http.Client
	host   *PluginHost
}

func (h *HomeAssistant) providerHost() *PluginHost {
	if h.host != nil {
		return h.host
	}
	return pluginHost
}

func (h *HomeAssistant) ready() bool {
	if h == nil || h.config == nil {
		return false
	}
	c := h.config()
	if c.URL == "" || c.Token == "" {
		return false
	}
	_, err := h.providerHost().Provider("homeassistant")
	return err == nil
}
func (h *HomeAssistant) call(ctx context.Context, op string, args any, out any) error {
	return serviceCallHost(ctx, h.providerHost(), "homeassistant", "homeassistant", map[string]any{"op": op, "config": h.config(), "args": args}, out)
}
func (h *HomeAssistant) catalog(ctx context.Context, force bool) (out []HAEntity, err error) {
	err = h.call(ctx, "catalog", force, &out)
	return
}
func (h *HomeAssistant) discoverRooms(ctx context.Context) (out []HARoomBinding, err error) {
	err = h.call(ctx, "rooms", nil, &out)
	return
}
func (h *HomeAssistant) control(ctx context.Context, args string) string {
	var out string
	if err := h.call(ctx, "control", args, &out); err != nil {
		return err.Error()
	}
	return out
}
func (h *HomeAssistant) vacuumCommand(ctx context.Context, text string) string {
	var out string
	if err := h.call(ctx, "command", text, &out); err != nil {
		return err.Error()
	}
	return out
}
func (h *HomeAssistant) vacuumIntent(text string) bool {
	if !h.ready() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bool
	_ = h.call(ctx, "intent", text, &out)
	return out
}
func (h *HomeAssistant) alive() bool {
	if !h.ready() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out bool
	_ = h.call(ctx, "alive", nil, &out)
	return out
}
func (h *HomeAssistant) context() string {
	if !h.ready() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var out string
	_ = h.call(ctx, "context", nil, &out)
	return out
}
func (h *HomeAssistant) invalidate() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = h.call(ctx, "invalidate", nil, nil)
}

func validateHAURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("укажи адрес Home Assistant, например http://192.168.1.10:8123")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func haDefinition() map[string]any {
	return map[string]any{"type": "function", "name": "home_assistant", "description": "Устройства сервера Home Assistant (только они; Xiaomi Home и Smart Life — своими инструментами): состояние, включение, выключение, пылесос и уборка выбранной комнаты. Комнату передавай по имени; без настроенного соответствия уборку не запускай.", "parameters": map[string]any{"type": "object", "properties": map[string]any{
		"entity_id": map[string]any{"type": "string", "description": "реальный entity_id из списка устройств; для clean_room можно не указывать, если соответствие единственное"},
		"action":    map[string]any{"type": "string", "enum": []string{"status", "turn_on", "turn_off", "start", "pause", "stop", "return_to_base", "clean_room"}},
		"room":      map[string]any{"type": "string", "description": "название комнаты для уборки"}}, "required": []string{"action"}}}
}
