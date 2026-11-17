package main

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
)

func (h *HomeAssistant) vacuumIntent(text string) bool {
	if !h.ready() {
		return false
	}
	n := strings.TrimSpace(leadRe.ReplaceAllString(normText(text), ""))
	verb := false
	for _, v := range []string{"включи ", "запусти ", "убери ", "пропылесось ", "начни ", "останови ", "выключи ", "выруби ", "отключи ", "прекрати ", "продолжи ", "поставь ", "верни ", "отправь ", "что с ", "где ", "как там "} {
		if strings.HasPrefix(n, v) {
			verb = true
			break
		}
	}
	if !verb {
		return false
	}
	if strings.Contains(n, "пылесос") || strings.Contains(n, "пылик") || strings.Contains(n, "уборк") {
		return true
	}
	if strings.HasPrefix(n, "убери ") || strings.HasPrefix(n, "пропылесось ") {
		for _, r := range h.config().Rooms {
			if haBindingMention(n, r) {
				return true
			}
		}
		for _, word := range []string{"кухн", "спальн", "комнат", "гостин", "коридор", "прихож", "ванн", "туалет", "кабинет", " зал"} {
			if strings.Contains(n, word) {
				return true
			}
		}
	}
	return false
}

func haRoomMention(text, name string) bool {
	// Match ordinary Russian case endings, preserving whole words.
	stem := func(s string) string {
		return strings.TrimRightFunc(s, func(r rune) bool { return strings.ContainsRune("аяуюеёиыоьй", r) })
	}
	words := strings.Fields(normText(text))
	need := strings.Fields(normText(name))
	if len(need) == 0 {
		return false
	}
	for _, n := range need {
		found := false
		for _, w := range words {
			if w == n || (len([]rune(n)) >= 4 && stem(w) == stem(n)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (h *HomeAssistant) vacuumCommand(ctx context.Context, text string) string {
	n := strings.TrimSpace(leadRe.ReplaceAllString(normText(text), ""))
	action := "start"
	switch {
	case strings.Contains(n, "на баз") || strings.Contains(n, "на заряд"):
		action = "return_to_base"
	case strings.Contains(n, "пауз"):
		action = "pause"
	case strings.HasPrefix(n, "останови ") || strings.HasPrefix(n, "выключи ") || strings.HasPrefix(n, "выруби ") || strings.HasPrefix(n, "отключи ") || strings.HasPrefix(n, "прекрати "):
		action = "stop"
	case strings.HasPrefix(n, "где ") || strings.HasPrefix(n, "что с ") || strings.HasPrefix(n, "как там "):
		action = "status"
	}
	entities, err := h.catalog(ctx, false)
	if err != nil {
		return err.Error()
	}
	var vacuums []HAEntity
	for _, e := range entities {
		if strings.HasPrefix(e.ID, "vacuum.") {
			vacuums = append(vacuums, e)
		}
	}
	entity := ""
	if len(vacuums) == 1 {
		entity = vacuums[0].ID
	} else {
		for _, e := range vacuums {
			if strings.Contains(n, normText(e.name())) {
				if entity != "" {
					return "Уточни, какой пылесос."
				}
				entity = e.ID
			}
		}
	}
	room := ""
	if action == "start" {
		for _, r := range h.config().Rooms {
			if haBindingMention(n, r) && (entity == "" || r.Vacuum == "" || r.Vacuum == entity) {
				if room != "" && room != r.Name {
					return "Пока назови одну комнату для уборки."
				}
				room = r.Name
			}
		}
		if room != "" {
			action = "clean_room"
		} else {
			// Any requested location must be resolved explicitly; never fall back to start.
			hasLocation := strings.HasPrefix(n, "убери ") || strings.HasPrefix(n, "пропылесось ")
			for _, w := range []string{" в ", " на кухн", "комнат", "спальн", "коридор", "гостин"} {
				hasLocation = hasLocation || strings.Contains(" "+n+" ", w)
			}
			if hasLocation {
				return "Для этой комнаты ещё не настроена уборка в подключении Home Assistant. Всю квартиру вместо неё не запускаю."
			}
		}
	}
	if entity == "" && action != "clean_room" {
		if len(vacuums) == 0 {
			return "Home Assistant подключён, но пылесос ещё не появился. Заверши вход Xiaomi Home и выбери устройства."
		}
		return "В Home Assistant несколько пылесосов. Назови нужный."
	}
	// Reject accidental control requests hidden in longer questions.
	if len(strings.Fields(n)) > 18 || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		return "Скажи коротко, что сделать с пылесосом."
	}
	args, _ := json.Marshal(map[string]string{"entity_id": entity, "action": action, "room": room})
	return h.control(ctx, string(args))
}
