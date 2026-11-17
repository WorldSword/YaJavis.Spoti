package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

type haMapRooms struct {
	Rooms []struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	} `json:"rooms"`
	MapUID any `json:"map_uid"`
}

func parseHARooms(e HAEntity) (haMapRooms, bool) {
	var out haMapRooms
	raw := []byte(e.State)
	if rooms, ok := e.Attributes["rooms"]; ok {
		raw, _ = json.Marshal(map[string]any{"rooms": rooms, "map_uid": e.Attributes["map_uid"]})
	}
	err := json.Unmarshal(raw, &out)
	return out, err == nil && len(out.Rooms) > 0
}

func (h *HomeAssistant) deviceEntities(ctx context.Context) (map[string]string, error) {
	c := h.config()
	base, err := validateHAURL(c.URL)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(base)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/api/websocket"
	ws, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: h.client})
	if err != nil {
		return nil, errors.New("не удалось получить список устройств Home Assistant")
	}
	defer ws.CloseNow()
	ws.SetReadLimit(8 << 20)
	read := func(v any) error {
		_, b, e := ws.Read(ctx)
		if e != nil {
			return e
		}
		return json.Unmarshal(b, v)
	}
	write := func(v any) error { b, _ := json.Marshal(v); return ws.Write(ctx, websocket.MessageText, b) }
	var hello struct{ Type string }
	if err = read(&hello); err != nil {
		return nil, err
	}
	if err = write(map[string]any{"type": "auth", "access_token": c.Token}); err != nil {
		return nil, err
	}
	if err = read(&hello); err != nil || hello.Type != "auth_ok" {
		return nil, errors.New("Home Assistant не принял вход для списка устройств")
	}
	if err = write(map[string]any{"id": 1, "type": "config/entity_registry/list"}); err != nil {
		return nil, err
	}
	var reply struct {
		Success bool
		Result  []struct {
			ID       string `json:"entity_id"`
			DeviceID string `json:"device_id"`
		}
	}
	if err = read(&reply); err != nil || !reply.Success {
		return nil, errors.New("Home Assistant не вернул реестр устройств")
	}
	devices := map[string]string{}
	for _, e := range reply.Result {
		devices[e.ID] = e.DeviceID
	}
	return devices, nil
}

func (h *HomeAssistant) discoverRooms(ctx context.Context) ([]HARoomBinding, error) {
	entities, err := h.catalog(ctx, true)
	if err != nil {
		return nil, err
	}
	devices, err := h.deviceEntities(ctx)
	if err != nil {
		return nil, err
	}
	var bindings []HARoomBinding
	for _, v := range entities {
		if !strings.HasPrefix(v.ID, "vacuum.") || devices[v.ID] == "" {
			continue
		}
		var info HAEntity
		action, result := "", ""
		for _, e := range entities {
			if devices[e.ID] != devices[v.ID] {
				continue
			}
			if strings.HasPrefix(e.ID, "sensor.") && strings.Contains(e.ID, "room_information") {
				if _, ok := parseHARooms(e); ok {
					info = e
				}
			}
			if strings.HasPrefix(e.ID, "notify.") && strings.Contains(e.ID, "start_vacuum_room_sweep") {
				action = e.ID
			}
		}
		if action == "" {
			for _, e := range entities {
				if devices[e.ID] == devices[v.ID] && strings.HasPrefix(e.ID, "text.") && strings.Contains(e.ID, "start_vacuum_room_sweep") {
					action = e.ID
					break
				}
			}
		}
		if info.ID == "" || action == "" {
			continue
		}
		siid := ""
		if _, suffix, ok := strings.Cut(action, "_a_"); ok {
			siid, _, _ = strings.Cut(suffix, "_")
		}
		for _, e := range entities {
			if devices[e.ID] == devices[v.ID] && strings.HasPrefix(e.ID, "sensor.") && siid != "" && strings.Contains(e.ID, "_action_result_p_"+siid+"_") {
				result = e.ID
				break
			}
		}
		rooms, _ := parseHARooms(info)
		for _, r := range rooms.Rooms {
			id := fmt.Sprint(r.ID)
			if r.Name == "" || id == "" || id == "<nil>" {
				continue
			}
			name, aliases := haRoomName(r.Name)
			bindings = append(bindings, HARoomBinding{Name: name, Aliases: aliases, Vacuum: v.ID, Entity: action, RoomIDs: id, InfoEntity: info.ID, InfoRoom: r.Name, ResultEntity: result})
		}
	}
	return bindings, nil
}

func haRoomName(s string) (string, []string) {
	switch normText(s) {
	case "living room":
		return "Гостиная", []string{s, "зал"}
	case "bedroom":
		return "Спальня", []string{s}
	case "kitchen":
		return "Кухня", []string{s}
	case "corridor", "hallway":
		return "Коридор", []string{s}
	case "bathroom":
		return "Ванная", []string{s}
	case "toilet":
		return "Туалет", []string{s}
	case "office", "study":
		return "Кабинет", []string{s}
	}
	return s, nil
}

func haBindingName(r HARoomBinding, name string) bool {
	if normText(r.Name) == normText(name) {
		return true
	}
	for _, a := range r.Aliases {
		if normText(a) == normText(name) {
			return true
		}
	}
	return false
}
func haBindingMention(text string, r HARoomBinding) bool {
	if haRoomMention(text, r.Name) {
		return true
	}
	for _, a := range r.Aliases {
		if haRoomMention(text, a) {
			return true
		}
	}
	return false
}
