package main

// Инструменты для мозга Джарвиса — Яндекс Умный дом. Модель получает список устройств с их
// возможностями (вкл/выкл, яркость, цвет, температура света, режимы, пауза, температура кондиционера)
// и сценарии с голосовыми фразами, а сама выбирает id. Простые команды колонка делает без модели (fastcmd.go).

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type Tools struct {
	HomeAssistant *HomeAssistant
	Xiaomi        *Xiaomi
	SmartLife     *SmartLife
	Room          func() string                                  // комната колонки из настроек
	Use           func(ch string) bool                           // галочки «через что управлять домом» (настройки колонки)
	Music         func(ctx context.Context, query string) string // включить музыку с YouTube (nil — нельзя)
	// MusicCtl — управлять тем, что играет с YouTube: пауза, продолжить, перемотка, следующее, стоп, что играет
	MusicCtl func(ctx context.Context, command string) string
	// SpotifyPlay — найти и включить в Spotify через Soloist (nil — Soloist не найден, инструмент не объявляется)
	SpotifyPlay func(ctx context.Context, query string) string
	// MusicQueue — добавить в очередь YouTube (если ничего не играет — включить)
	MusicQueue func(ctx context.Context, query string) string
	// Recall — поиск по архиву прошлых разговоров («вспомни, что ты советовал про…»); nil — нет памяти
	Recall func(query, date string) string
	// MemoryEdit — «забудь / запомни / исправь» в долгой памяти
	MemoryEdit func(forget, remember string, words []string) string
	token      func() string
	now        func() time.Time
	client     *http.Client
}

func NewTools(token func() string, client *http.Client) *Tools {
	return &Tools{token: token, client: client, now: time.Now}
}

func (t *Tools) yandexCall(ctx context.Context, op string, args, out any) error {
	return serviceCall(ctx, "yandexhome", "yandexhome", map[string]any{"op": op, "args": args, "token": t.token(), "room": t.room(), "alice": t.use("alice")}, out)
}
func (t *Tools) hasCatalog() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bool
	_ = t.yandexCall(ctx, "has-catalog", nil, &out)
	return out
}
func (t *Tools) Prewarm() {
	if t.token() == "" || !t.use("yandex") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_ = t.yandexCall(ctx, "prewarm", nil, nil)
}
func (t *Tools) Fast(ctx context.Context, text string) (bool, string) {
	if t.token() == "" || !t.use("yandex") {
		return false, ""
	}
	var out struct {
		Handled bool   `json:"handled"`
		Answer  string `json:"answer"`
	}
	if err := t.yandexCall(ctx, "fast", text, &out); err != nil {
		return false, ""
	}
	return out.Handled, out.Answer
}

// Context — текст для инструкций бэкенда: время, устройства с возможностями, сценарии с фразами.
func (t *Tools) Context() string {
	var b strings.Builder
	now := t.now()
	days := []string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
	b.WriteString("\n\nСейчас " + now.Format("02.01.2006 15:04") + ", " + days[now.Weekday()] + ".")
	if t.HomeAssistant != nil && t.use("homeassistant") && t.HomeAssistant.alive() {
		b.WriteString(t.HomeAssistant.context())
	}
	if t.use("xiaomi") {
		b.WriteString(t.Xiaomi.context())
	}
	if t.use("smartlife") {
		b.WriteString(t.SmartLife.context())
	}
	room := t.room()
	if !t.use("yandex") {
		if room != "" {
			b.WriteString(roomLine(room))
		}
		return b.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out string
	if err := t.yandexCall(ctx, "context", nil, &out); err == nil {
		b.WriteString(out)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (t *Tools) Definitions() []any {
	defs := []any{}
	if t.HomeAssistant.ready() && t.use("homeassistant") && t.HomeAssistant.alive() {
		defs = append(defs, haDefinition())
	}
	if t.Xiaomi.ready() && len(t.Xiaomi.Devices()) > 0 && t.use("xiaomi") {
		defs = append(defs, xiaomiDefinition())
	}
	if t.SmartLife.ready() && len(t.SmartLife.Devices()) > 0 && t.use("smartlife") {
		defs = append(defs, smartLifeDefinition())
	}
	if t.Music != nil {
		who := "Обычную музыку по названию включает Алиса, но если просят включить что-то на колонке тебя («что-нибудь интересное», «чтобы болтало на фоне», «с ютуба») — " +
			"вызывай сразу, запрос придумай сам."
		if !t.use("yandexmusic") {
			who = "Яндекс Музыка выключена в настройках: любую музыку, песню или радио включай только этим инструментом, сразу, запрос придумай сам, если не назвали."
		}
		defs = append(defs, map[string]any{"type": "function", "name": "play_music",
			"description": "Найти по всему YouTube и включить звук видео на колонке: любой ролик, кавер, ремикс, концерт, подкаст или песню. " + who +
				" Не обещай «сейчас поищу» без вызова.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "что искать: исполнитель и название"},
				"queue": map[string]any{"type": "boolean", "description": "true — не включать сразу, а добавить в очередь после того, что играет («добавь в очередь», «потом включи»)"}},
				"required": []any{"query"}}})
	}
	if t.SpotifyPlay != nil {
		defs = append(defs, map[string]any{"type": "function", "name": "play_spotify",
			"description": "Найти и включить трек, альбом или исполнителя в Spotify на колонке. Вызывай на любую просьбу со словом Spotify, Спотифай или Спотик " +
				"(«включи в Спотифае Кино»); запрос — исполнитель и название. Не обещай «сейчас поищу» без вызова.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "что искать: исполнитель и название"}},
				"required": []any{"query"}}})
	}
	if t.MusicCtl != nil {
		defs = append(defs, map[string]any{"type": "function", "name": "music_control",
			"description": "Управлять тем, что сейчас играет с YouTube или в Spotify: пауза, продолжить, перемотка вперёд/назад, с начала, следующее видео, похожее (радио), выключить, что играет.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "команда по-русски, например «пауза», «продолжи», «перемотай вперёд на 2 минуты», «назад на 30 секунд», «сначала», «следующее», «включи похожее», «выключи музыку», «что играет»"}},
				"required": []any{"command"}}})
	}
	if t.use("alice") {
		defs = append(defs, aliceDefinition())
	}
	if t.Recall != nil && chatgptChat.Enabled() {
		defs = append(defs, recallDefinition())
	}
	if t.MemoryEdit != nil && chatgptChat.Enabled() {
		defs = append(defs, memoryEditDefinition())
	}
	defs = append(defs, pluginHost.Tools()...)
	if t.token() == "" || !t.use("yandex") || !t.hasCatalog() {
		return defs // список устройств Яндекса не загрузился — без него модель зовёт инструмент вслепую; пусть передаёт Алисе
	}
	return append(defs, t.homeDefinition())
}

func (t *Tools) homeDefinition() any {
	return map[string]any{"type": "function", "name": "smart_home",
		"description": "Яндекс Умный дом: включить/выключить, яркость, цвет, температура белого, режимы (пылесос, кондиционер), пауза, температура кондиционера; или запустить сценарий. Указывай только то, что просили.",
		"parameters": map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"device_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "id устройств или групп"},
				"on":            map[string]any{"type": "boolean", "description": "true — включить (пылесос: начать уборку), false — выключить (пылесос: на базу)"},
				"brightness":    map[string]any{"type": "integer", "description": "яркость 1–100"},
				"color":         map[string]any{"type": "string", "description": "цвет #RRGGBB (красный #FF0000 и т. п.)"},
				"temperature_k": map[string]any{"type": "integer", "description": "температура белого света в кельвинах: тёплый 2700, нейтральный 4500, холодный 6500"},
				"modes":         map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "режимы {instance: value}, например {\"cleanup_mode\": \"wet_cleaning\", \"work_speed\": \"turbo\"} или {\"thermostat\": \"cool\"}"},
				"toggles":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "boolean"}, "description": "переключатели {instance: true/false}, например {\"pause\": true}"},
				"ranges":        map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "number"}, "description": "числовые параметры {instance: value}, например {\"temperature\": 22}"},
				"scenario_id":   map[string]any{"type": "string", "description": "id сценария, если просили его запустить (тогда остальное не нужно)"},
			}}}
}

func (t *Tools) Call(ctx context.Context, name, args string) string {
	if name == "home_assistant" && t.HomeAssistant.ready() && t.use("homeassistant") {
		return t.HomeAssistant.control(ctx, args)
	}
	if name == "xiaomi_home" && t.Xiaomi.ready() {
		return t.Xiaomi.control(ctx, args)
	}
	if name == "smart_life" && t.SmartLife.ready() {
		return t.SmartLife.control(ctx, args)
	}
	if name == "play_music" && t.Music != nil {
		var a struct {
			Query string
			Queue bool
		}
		if json.Unmarshal([]byte(args), &a) != nil || strings.TrimSpace(a.Query) == "" {
			return "Не понял, что включить."
		}
		if a.Queue && !recommendationRequest(a.Query) && t.MusicQueue != nil {
			return t.MusicQueue(ctx, a.Query)
		}
		return t.Music(ctx, a.Query)
	}
	if name == "play_spotify" && t.SpotifyPlay != nil {
		var a struct{ Query string }
		if json.Unmarshal([]byte(args), &a) != nil || strings.TrimSpace(a.Query) == "" {
			return "Не понял, что включить в Spotify."
		}
		return t.SpotifyPlay(ctx, a.Query)
	}
	if name == "music_control" && t.MusicCtl != nil {
		var a struct{ Command string }
		if json.Unmarshal([]byte(args), &a) != nil || strings.TrimSpace(a.Command) == "" {
			return "Не понял команду для музыки."
		}
		return t.MusicCtl(ctx, a.Command)
	}
	if name == "recall_conversation" && t.Recall != nil {
		var a struct{ Query, Date string }
		_ = json.Unmarshal([]byte(args), &a)
		if strings.TrimSpace(a.Query) == "" && strings.TrimSpace(a.Date) == "" {
			return "Скажи, что вспомнить: ключевые слова или день."
		}
		return t.Recall(a.Query, a.Date)
	}
	if name == "edit_memory" && t.MemoryEdit != nil {
		var a struct {
			Forget, Remember string
			Words            []string
		}
		_ = json.Unmarshal([]byte(args), &a)
		return t.MemoryEdit(a.Forget, a.Remember, a.Words)
	}
	if name == "alice_command" && t.use("alice") {
		var a struct{ Command string }
		if json.Unmarshal([]byte(args), &a) != nil || strings.TrimSpace(a.Command) == "" {
			return "Не понял команду для Алисы."
		}
		cmd := strings.TrimSpace(a.Command)
		if err := AliceText(cmd, true); err != nil { // беззвучно: результат озвучит сам Джарвис
			return "Алиса недоступна: " + err.Error()
		}
		log.Printf("инструмент: «%s» → Алисе", cmd)
		return "Передал Алисе: «" + cmd + "». Она выполняет — скажи коротко, что сделано."
	}
	if name != "smart_home" {
		if res, ok := pluginHost.CallTool(ctx, name, args); ok {
			log.Printf("инструмент плагина %s → %s", name, clip(res, 200))
			return res
		}
		return "Нет такого инструмента."
	}
	if !t.use("yandex") {
		return "Управление через Яндекс Умный дом выключено."
	}
	var out string
	if err := t.yandexCall(ctx, "control", args, &out); err != nil {
		return err.Error()
	}
	return out
}

func aliceDefinition() any {
	return map[string]any{"type": "function", "name": "alice_command",
		"description": "Передать команду штатной Алисе на этой колонке — она выполнит всё, что подключено к Яндексу: свет, шторы, розетки, " +
			"сценарии, таймеры, будильники, напоминания. Таймеры, будильники и напоминания на этой колонке ставятся только так — " +
			"никогда не отвечай, что не умеешь их ставить. Пиши команду коротко, как её сказали бы Алисе: «закрой шторы», «поставь таймер на 10 минут».",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "команда для Алисы, без её имени"}},
			"required": []any{"command"}}}
}

func (t *Tools) use(ch string) bool {
	if ch == "yandex" {
		if _, err := pluginHost.Provider("yandexhome"); err != nil {
			return false
		}
	}
	return t.Use == nil || t.Use(ch)
}

func (t *Tools) room() string {
	if t.Room == nil {
		return ""
	}
	return strings.TrimSpace(t.Room())
}

func roomLine(room string) string {
	return "\nКолонка стоит в комнате «" + room + "». Если хозяин не назвал комнату («выключи свет», «включи кондиционер») — " +
		"имеется в виду эта комната; «весь свет» — все лампы и выключатели света во всём доме."
}
