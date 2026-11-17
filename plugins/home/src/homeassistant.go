package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
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

type HomeAssistant struct {
	config   func() HASettings
	client   *http.Client
	mu       sync.Mutex
	entities []HAEntity
	fetched  time.Time
	failed   time.Time // последняя неудача: минуту не предлагаем модели недоступный сервер
}

func (h *HomeAssistant) ready() bool {
	return h != nil && h.config().URL != "" && h.config().Token != ""
}
func validateHAURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("укажи адрес Home Assistant, например http://192.168.1.10:8123")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
func (h *HomeAssistant) request(ctx context.Context, method, path string, body, out any) error {
	cfg := h.config()
	if cfg.Token == "" {
		return errors.New("Home Assistant не подключён")
	}
	base, err := validateHAURL(cfg.URL)
	if err != nil {
		return err
	}
	var b []byte
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/api/"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return errors.New("Home Assistant недоступен — проверь адрес и включён ли сервер")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Home Assistant ответил %d", resp.StatusCode)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
func (h *HomeAssistant) catalog(ctx context.Context, force bool) ([]HAEntity, error) {
	h.mu.Lock()
	if !force && time.Since(h.fetched) < 30*time.Second && h.entities != nil {
		e := h.entities
		h.mu.Unlock()
		return e, nil
	}
	h.mu.Unlock()
	var entities []HAEntity
	if err := h.request(ctx, http.MethodGet, "states", nil, &entities); err != nil {
		h.mu.Lock()
		h.failed = time.Now()
		h.mu.Unlock()
		return nil, err
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
	h.mu.Lock()
	h.entities, h.fetched = entities, time.Now()
	h.mu.Unlock()
	return entities, nil
}
func (h *HomeAssistant) context() string {
	if !h.ready() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entities, err := h.catalog(ctx, false)
	if err != nil {
		return "\nHome Assistant подключён, но сейчас недоступен. Не утверждай, что действия выполнены."
	}
	var b strings.Builder
	b.WriteString("\n\nHome Assistant: используй home_assistant только для entity_id ниже. Для уборки определённой комнаты используй clean_room и её название; НИКОГДА не заменяй такую просьбу уборкой всей квартиры. Не выдумывай номера комнат и не передавай команды Xiaomi в smart_home Яндекса.\n")
	n := 0
	for _, e := range entities {
		domain, _, _ := strings.Cut(e.ID, ".")
		switch domain {
		case "vacuum", "script", "scene", "light", "switch", "fan", "climate", "cover":
			fmt.Fprintf(&b, "%s «%s»: %s\n", e.ID, e.name(), e.State)
			n++
			if n >= 80 {
				break
			}
		}
		if n >= 80 {
			break
		}
	}
	for _, r := range h.config().Rooms {
		fmt.Fprintf(&b, "Доступна уборка комнаты «%s» (пылесос %s).\n", r.Name, r.Vacuum)
	}
	return b.String()
}
func haDefinition() map[string]any {
	return map[string]any{"type": "function", "name": "home_assistant", "description": "Устройства сервера Home Assistant (только они; Xiaomi Home и Smart Life — своими инструментами): состояние, включение, выключение, пылесос и уборка выбранной комнаты. Комнату передавай по имени; без настроенного соответствия уборку не запускай.", "parameters": map[string]any{"type": "object", "properties": map[string]any{
		"entity_id": map[string]any{"type": "string", "description": "реальный entity_id из списка устройств; для clean_room можно не указывать, если соответствие единственное"},
		"action":    map[string]any{"type": "string", "enum": []string{"status", "turn_on", "turn_off", "start", "pause", "stop", "return_to_base", "clean_room"}},
		"room":      map[string]any{"type": "string", "description": "название комнаты для уборки"}}, "required": []string{"action"}}}
}
func (h *HomeAssistant) control(ctx context.Context, args string) string {
	var a struct {
		Entity string `json:"entity_id"`
		Action string `json:"action"`
		Room   string `json:"room"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return "Не понял команду Home Assistant."
	}
	entities, err := h.catalog(ctx, true)
	if err != nil {
		return err.Error()
	}
	known := map[string]HAEntity{}
	for _, e := range entities {
		known[e.ID] = e
	}
	domain, _, _ := strings.Cut(a.Entity, ".")
	service := ""
	data := map[string]any{"entity_id": a.Entity}
	resultEntity := ""
	resultVacuum := ""
	if a.Action == "clean_room" {
		if strings.TrimSpace(a.Room) == "" {
			return "Какую комнату убрать?"
		}
		var bindings []HARoomBinding
		for _, r := range h.config().Rooms {
			if haBindingName(r, a.Room) && (a.Entity == "" || r.Vacuum == a.Entity) {
				bindings = append(bindings, r)
			}
		}
		if len(bindings) == 0 {
			return "Уборка комнаты «" + a.Room + "» ещё не настроена для этого пылесоса. Всю квартиру вместо неё не запускаю."
		}
		if len(bindings) > 1 {
			return "Для этой комнаты несколько пылесосов. Уточни устройство."
		}
		r := bindings[0]
		resultEntity = r.ResultEntity
		resultVacuum = r.Vacuum
		if r.InfoEntity != "" {
			rooms, ok := parseHARooms(known[r.InfoEntity])
			if !ok {
				return "Не получилось прочитать актуальную карту комнат пылесоса."
			}
			found := false
			for _, room := range rooms.Rooms {
				if room.Name == r.InfoRoom {
					r.RoomIDs = fmt.Sprint(room.ID)
					found = true
					break
				}
			}
			if !found {
				return "Комната на карте пылесоса изменилась. Обнови список комнат в подключении Home Assistant."
			}
		}
		a.Entity = r.Entity
		domain, _, _ = strings.Cut(r.Entity, ".")
		data = map[string]any{"entity_id": r.Entity}
		switch domain {
		case "script":
			service = "turn_on"
		case "notify", "text":
			if r.RoomIDs == "" {
				return "У комнаты не указаны номера зон на карте пылесоса."
			}
			// Xiaomi action debug parses YAML: a room list must stay a string.
			value := "'" + strings.ReplaceAll(r.RoomIDs, "'", "''") + "'"
			if domain == "notify" {
				service = "send_message"
				data["message"] = value
			} else {
				service = "set_value"
				data["value"] = value
			}
		default:
			return "Для комнаты нужен сценарий уборки или действие пылесоса из Home Assistant."
		}
	} else {
		switch a.Action {
		case "status":
			e, ok := known[a.Entity]
			if !ok {
				return "Устройство Home Assistant не найдено."
			}
			return fmt.Sprintf("%s: %s.", e.name(), haStateText(e.State))
		case "turn_on", "turn_off":
			switch domain {
			case "light", "switch", "fan", "climate":
				service = a.Action
			case "script", "scene":
				if a.Action == "turn_on" {
					service = "turn_on"
				}
			}
		case "start", "pause", "stop", "return_to_base":
			if domain == "vacuum" {
				service = a.Action
			}
		}
	}
	e, ok := known[a.Entity]
	if !ok {
		return "Устройство или сценарий Home Assistant не найден. Обнови подключение."
	}
	if e.State == "unavailable" {
		return "Устройство «" + e.name() + "» сейчас недоступно."
	}
	if service == "" {
		return "Такое действие для этого устройства не поддерживается."
	}
	requestedAt := time.Now()
	if domain == "vacuum" && a.Action == "stop" {
		// A dock may keep washing/drying after vacuum.stop. Stop dock jobs first,
		// then stop the robot again because cancelling a wash can resume its job.
		_ = h.request(ctx, http.MethodPost, "services/vacuum/pause", data, nil)
		h.stopVacuumBase(ctx, a.Entity, entities)
		_ = h.request(ctx, http.MethodPost, "services/vacuum/pause", data, nil)
	}
	if err := h.request(ctx, http.MethodPost, "services/"+domain+"/"+service, data, nil); err != nil {
		return "Команда не выполнена: " + err.Error()
	}
	h.mu.Lock()
	h.fetched = time.Time{}
	h.mu.Unlock()
	if a.Action == "clean_room" {
		if resultEntity != "" {
			checkCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			for checkCtx.Err() == nil {
				var result HAEntity
				if resultVacuum != "" {
					var robot HAEntity
					if h.request(checkCtx, http.MethodGet, "states/"+resultVacuum, nil, &robot) == nil && robot.State == "cleaning" && robot.LastUpdated.After(requestedAt) {
						return "Пылесос начал уборку по команде для комнаты «" + a.Room + "»."
					}
				}
				if h.request(checkCtx, http.MethodGet, "states/"+resultEntity, nil, &result) == nil && (result.LastUpdated.After(requestedAt) || result.LastReported.After(requestedAt)) {
					var ack struct {
						Code *int `json:"code"`
					}
					if json.Unmarshal([]byte(result.State), &ack) == nil && ack.Code != nil {
						if *ack.Code == 0 {
							return "Пылесос подтвердил команду уборки комнаты «" + a.Room + "»."
						}
						return fmt.Sprintf("Пылесос отклонил уборку комнаты «%s», код %d.", a.Room, *ack.Code)
					}
				}
				select {
				case <-checkCtx.Done():
				case <-time.After(400 * time.Millisecond):
				}
			}
		}
		return "Команда уборки комнаты «" + a.Room + "» передана. Жду подтверждения от пылесоса."
	}
	if domain == "vacuum" || domain == "light" || domain == "switch" {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for checkCtx.Err() == nil {
			var current HAEntity
			if h.request(checkCtx, http.MethodGet, "states/"+a.Entity, nil, &current) == nil {
				match := (a.Action == "start" && current.State == "cleaning") || (a.Action == "pause" && current.State == "paused") ||
					(a.Action == "stop" && (current.State == "idle" || current.State == "docked" || current.State == "paused")) ||
					(a.Action == "return_to_base" && (current.State == "returning" || current.State == "docked")) ||
					(a.Action == "turn_on" && current.State == "on") || (a.Action == "turn_off" && current.State == "off")
				if match {
					return current.name() + ": " + haStateText(current.State) + "."
				}
				if a.Action == "return_to_base" && current.State == "idle" {
					all, _ := h.catalog(checkCtx, true)
					prefix := "sensor." + strings.TrimPrefix(a.Entity, "vacuum.") + "_charging_state_"
					for _, charge := range all {
						if strings.HasPrefix(charge.ID, prefix) && strings.EqualFold(charge.State, "charging") {
							return current.name() + ": на базе, заряжается."
						}
					}
				}
			}
			select {
			case <-checkCtx.Done():
			case <-time.After(400 * time.Millisecond):
			}
		}
	}
	return "Команда для «" + e.name() + "» передана в Home Assistant. Подтверждения от устройства пока нет."
}

func haStateText(s string) string {
	switch s {
	case "cleaning":
		return "убирает"
	case "docked":
		return "на базе"
	case "returning":
		return "возвращается на базу"
	case "paused":
		return "на паузе"
	case "idle":
		return "ожидает"
	case "on":
		return "включено"
	case "off":
		return "выключено"
	case "unavailable":
		return "недоступно"
	case "unknown":
		return "состояние пока неизвестно"
	}
	return s
}

// alive — сервер отвечал (или ещё не пробовали); недоступный сервер модели не предлагаем, иначе она зовёт его вместо Xiaomi.
func (h *HomeAssistant) alive() bool {
	if !h.ready() {
		return false
	}
	h.mu.Lock()
	recent := !h.failed.IsZero() && time.Since(h.failed) < time.Minute && h.failed.After(h.fetched)
	h.mu.Unlock()
	if recent {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := h.catalog(ctx, false)
	return err == nil
}
