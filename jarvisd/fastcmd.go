package main

// Быстрые команды без GPT: «включи свет», «выключи стол», «выключи весь свет», «свет на кухне»,
// «запусти сценарий …», «стоп». Распознаются по расшифровке речи и выполняются сразу — без ожидания,
// пока модель подумает. Сопоставление названий — как в старом серверном Джарвисе.

import (
	"context"
	"math"
	"regexp"
	"strings"
)

var (
	nonWord     = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
	fastCmdRe   = regexp.MustCompile(`^(?:(?:эй|слушай|так|ну)\s+)?(?:джарвис\s+)?(включи|выключи|погаси|отключи|зажги|запусти сценарий|запусти|останови)\s+(.+?)(?:\s+пожалуйста)?$`)
	colorRe     = regexp.MustCompile(`^(?:(?:эй|слушай|так|ну)\s+)?(?:джарвис\s+)?(?:сделай|поставь|включи|переключи|измени|смени)\s+(?:цвет\s+)?(.+?)\s+(?:на\s+)?(красн|оранжев|желт|зелен|голуб|син|фиолетов|розов|бел|тепл|холодн|нейтральн)\p{L}*(?:\s+(?:цвет|свет)\p{L}*)?$`)
	leadRe      = regexp.MustCompile(`^(?:(?:эй|слушай|так|ну)\s+)?(?:джарвис\s+)?`)
	allLightsRe = regexp.MustCompile(`^(?:весь свет|все лампы|все освещение|свет везде|свет во всем доме|свет во всей квартире|везде свет)$`)
	roomSuffix  = regexp.MustCompile(`^(.+?)\s+(?:в|во|на)\s+(\S+)$`)
	// «стоп» — остановить всё (и музыку); «хватит», «закрой чат», «пока»… — закончить только разговор,
	// музыка, если играет, играет дальше
	stopRe = regexp.MustCompile(`^(?:(?:джарвис|ну|так|все|всё|спасибо)\s+)*(?:стоп|хватит|замолчи|заткнись|остановись|отбой|тихо|умолкни|прекрати|достаточно|пока|` +
		`(?:закрой|закончи|заверши|выключи)\s+(?:чат|разговор|диалог)|на этом все|можешь идти|свободен)(?:\s+(?:джарвис|все|пожалуйста|спасибо))*$`)
	stopAllRe = regexp.MustCompile(`(?:^|\s)(?:стоп|остановись|прекрати)(?:\s|$)`)
)

func normText(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	s = nonWord.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func isStop(text string) bool { return stopRe.MatchString(normText(text)) }

// isStopAll — «стоп»: остановить и разговор, и музыку. Остальные стоп-фразы закрывают только разговор.
func isStopAll(text string) bool {
	n := normText(text)
	return stopRe.MatchString(n) && stopAllRe.MatchString(n)
}

// «зал» ~ «зале», «кухня» ~ «кухне», «ванная» ~ «ванной»
func roomMatches(room, word string) bool {
	r := []rune(room)
	stem := room
	switch {
	case len(r) <= 3:
	case len(r) <= 5:
		stem = string(r[:len(r)-1])
	default:
		stem = string(r[:len(r)-2])
	}
	return word == room || strings.HasPrefix(word, stem)
}

func sameWord(a, b string) bool { // «ленту» ~ «лента», «лампу» ~ «лампа»
	ra, rb := []rune(a), []rune(b)
	return a == b || (len(ra) >= 4 && len(rb) >= 4 && string(ra[:len(ra)-1]) == string(rb[:len(rb)-1]))
}

type fastPlan struct {
	ids      []string
	on       bool
	scenario string
	what     string
}

var colorWords = map[string]homeArgs{
	"красн": {Color: "#FF0000"}, "оранжев": {Color: "#FF7A00"}, "желт": {Color: "#FFD000"}, "зелен": {Color: "#00FF20"},
	"голуб": {Color: "#00C8FF"}, "син": {Color: "#0030FF"}, "фиолетов": {Color: "#8A00FF"}, "розов": {Color: "#FF3CA0"},
	"бел": {TemperatureK: 4500}, "нейтральн": {TemperatureK: 4500}, "тепл": {TemperatureK: 2700}, "холодн": {TemperatureK: 6500},
}

// Fast — если фраза — сценарий или простая команда умного дома, выполнить её сразу. handled=false — пусть решает GPT.
func (t *Tools) Fast(ctx context.Context, text string) (handled bool, answer string) {
	if t.token() == "" {
		return false, ""
	}
	n := normText(text)
	c, err := t.Catalog(ctx)
	if err != nil {
		return false, ""
	}
	// 1) фраза сценария — как у Алисы («пора спать», «тихий кондей»)
	bare := leadRe.ReplaceAllString(n, "")
	for _, sc := range c.Scenarios {
		if sc.IsActive != nil && !*sc.IsActive {
			continue
		}
		for _, ph := range append(scenarioPhrases(sc), sc.Name) {
			if p := normText(ph); p != "" && (p == bare || p == n) {
				return true, t.Apply(ctx, &homeArgs{ScenarioID: sc.ID})
			}
		}
	}
	// 2) цвет: «сделай ленту красной», «лампу тёплым светом»
	if m := colorRe.FindStringSubmatch(n); m != nil {
		if plan := planCommand(c, "включи", m[1]); plan != nil && plan.scenario == "" {
			a := colorWords[m[2]]
			var ids []string
			for _, id := range plan.ids {
				for i := range c.Devices {
					if d := &c.Devices[i]; d.ID == id && d.cap("color_setting", "") != nil {
						ids = append(ids, id)
					}
				}
			}
			if len(ids) > 0 {
				a.DeviceIDs = ids
				return true, t.Apply(ctx, &a)
			}
		}
		return false, ""
	}
	// 3) включи/выключи/запусти/останови …
	m := fastCmdRe.FindStringSubmatch(n)
	if m == nil {
		return false, ""
	}
	plan := planCommand(c, m[1], m[2])
	if plan == nil {
		return false, "" // не нашли устройство — пусть разбирается модель
	}
	if plan.scenario != "" {
		return true, t.Apply(ctx, &homeArgs{ScenarioID: plan.scenario})
	}
	on := plan.on
	return true, t.Apply(ctx, &homeArgs{DeviceIDs: plan.ids, On: &on})
}

func planCommand(c *iotCatalog, verb, name string) *fastPlan {
	rooms := map[string]string{}
	for _, r := range c.Rooms {
		rooms[r.ID] = normText(r.Name)
	}
	if verb == "запусти сценарий" {
		for _, s := range c.Scenarios {
			if normText(s.Name) == name {
				return &fastPlan{scenario: s.ID, what: s.Name}
			}
		}
		return nil
	}
	on := verb == "включи" || verb == "зажги" || verb == "запусти"
	name = strings.TrimPrefix(strings.TrimPrefix(name, "устройство "), "группу ")

	// свет: «свет» — в комнате колонки, «свет на кухне», «весь свет»
	what, roomWord := name, ""
	if m := roomSuffix.FindStringSubmatch(name); m != nil {
		for _, r := range rooms {
			if roomMatches(r, m[2]) {
				what, roomWord = m[1], m[2]
				break
			}
		}
	}
	everywhere := allLightsRe.MatchString(what)
	if what == "свет" || everywhere {
		roomIDs := map[string]bool{}
		switch {
		case roomWord != "":
			for id, r := range rooms {
				if roomMatches(r, roomWord) {
					roomIDs[id] = true
				}
			}
		case !everywhere:
			for _, d := range c.Devices {
				if strings.HasPrefix(d.Type, "devices.types.smart_speaker") && d.Room != "" {
					roomIDs[d.Room] = true
				}
			}
			if len(roomIDs) == 0 {
				for id, r := range rooms {
					if r == "зал" || r == "гостиная" {
						roomIDs[id] = true
					}
				}
			}
		}
		var lights, named []string
		for _, d := range c.Devices {
			if !everywhere && !roomIDs[d.Room] || !isLight(d.Type, d.Name, d.Capabilities) {
				continue
			}
			lights = append(lights, d.ID)
			if normText(d.Name) == "свет" {
				named = append(named, d.ID)
			}
		}
		if !everywhere && len(named) > 0 {
			lights = named
		}
		if len(lights) == 0 {
			return nil
		}
		return &fastPlan{ids: lights, on: on, what: what}
	}

	// по названию: точное (с комнатой и без), потом по части слов («стол» → «Лента стол»)
	want := strings.Fields(what)
	type hit struct {
		id    string
		words int
	}
	var exact, partial []hit
	check := func(id, label, room string, extra ...string) {
		l := normText(label)
		aliases := append([]string{l}, extra...)
		if f := strings.Fields(l); len(f) > 0 {
			w := []rune(f[0])
			if last := w[len(w)-1]; last == 'а' || last == 'я' { // «лампа» → «лампу»
				end := map[rune]string{'а': "у", 'я': "ю"}[last]
				aliases = append(aliases, strings.Join(append([]string{string(w[:len(w)-1]) + end}, f[1:]...), " "))
			}
		}
		for _, a := range aliases {
			if a == what || (room != "" && a+" в "+room == name) || (roomWord != "" && a == what && roomMatches(room, roomWord)) {
				exact = append(exact, hit{id, len(strings.Fields(l))})
				return
			}
		}
		if roomWord != "" && !roomMatches(room, roomWord) {
			return
		}
		words := strings.Fields(l)
		all := len(want) > 0
		for _, w := range want {
			found := false
			for _, x := range words {
				if sameWord(w, x) {
					found = true
				}
			}
			all = all && found
		}
		if all {
			partial = append(partial, hit{id, len(words)})
		}
	}
	for _, d := range c.Devices {
		if hasOnOff(d.Capabilities) {
			check(d.ID, d.Name, rooms[d.Room], typeAliases(d.Type)...)
		}
	}
	for _, g := range c.Groups {
		check(g.ID, g.Name, "")
	}
	pick := exact
	if len(pick) == 0 {
		pick = partial
	}
	if len(pick) > 1 { // «лента» → «Лента», а не «Лента стол»
		best, n := []hit{}, math.MaxInt
		for _, h := range pick {
			if h.words < n {
				best, n = []hit{h}, h.words
			} else if h.words == n {
				best = append(best, h)
			}
		}
		pick = best
	}
	if len(pick) != 1 {
		return nil // нет или неоднозначно — пусть уточняет модель
	}
	return &fastPlan{ids: []string{pick[0].id}, on: on, what: what}
}

// typeAliases — как такое устройство называют, даже если в Яндексе оно «Mijia Robot Vacuum 5»
func typeAliases(typ string) []string {
	t := strings.TrimPrefix(typ, "devices.types.")
	switch {
	case strings.HasPrefix(t, "vacuum_cleaner"):
		return []string{"пылесос", "робот пылесос", "робот", "уборку", "уборка", "пылик"}
	case strings.HasPrefix(t, "thermostat.ac"):
		return []string{"кондиционер", "кондей", "кондер"}
	case strings.HasPrefix(t, "humidifier"):
		return []string{"увлажнитель"}
	case strings.HasPrefix(t, "purifier"):
		return []string{"очиститель"}
	case strings.HasPrefix(t, "media_device.tv"):
		return []string{"телевизор", "телек"}
	case strings.HasPrefix(t, "cooking.kettle"):
		return []string{"чайник"}
	}
	return nil
}

func hasOnOff(caps capList) bool {
	for _, c := range caps {
		if c.Type == "devices.capabilities.on_off" {
			return true
		}
	}
	return false
}

// свет — лампы и ленты, плюс выключатели, в названии которых есть «свет»/«лампа»/«люстра»/«лента»
func isLight(typ, name string, caps capList) bool {
	if !hasOnOff(caps) {
		return false
	}
	if strings.HasPrefix(typ, "devices.types.light") {
		return true
	}
	n := normText(name)
	return typ == "devices.types.switch" && (strings.Contains(n, "свет") || strings.Contains(n, "ламп") || strings.Contains(n, "люстр") || strings.Contains(n, "лент"))
}

// earcon — короткий звук «готово» (два тона вверх) или «не вышло» (вниз), 16 кГц.
func earcon(ok bool) []int16 {
	tones := [][2]float64{{880, 0.07}, {1320, 0.09}}
	if !ok {
		tones = [][2]float64{{660, 0.09}, {440, 0.14}}
	}
	var out []int16
	for _, t := range tones {
		n := int(t[1] * MicRate)
		for i := 0; i < n; i++ {
			env := math.Min(1, math.Min(float64(i), float64(n-i))/(0.008*MicRate))
			out = append(out, int16(9000*env*math.Sin(2*math.Pi*t[0]*float64(i)/MicRate)))
		}
		out = append(out, make([]int16, MicRate/50)...)
	}
	return append(out, make([]int16, MicRate/10)...)
}

var homeWords = regexp.MustCompile(`(свет|ламп|люстр|лент|торшер|пылесос|пылик|робот|кондиц|кондей|кондер|розетк|увлажнит|очистит|чайник|телевизор|яркост|сценари|штор|занавес|обогрев|вентилятор|температур)`)
var homeVerbs = regexp.MustCompile(`^(?:(?:эй|слушай|так|ну)\s+)?(?:джарвис\s+)?(?:включи|выключи|погаси|отключи|зажги|запусти|останови|сделай|поставь|убавь|прибавь|увеличь|уменьши|открой|закрой|отправь|верни)\s`)

// То, что умеет только штатная Алиса. Джарвис узнаёт такую просьбу и передаёт её Алисе целиком:
// она ставит таймер или включает музыку и отвечает сама, как обычно.
var (
	// «таймер на минуту», «будильник на семь» — без глагола тоже просьба к Алисе (раньше уходила модели, и та отвечала «не умею»)
	aliceVerbs  = regexp.MustCompile(`^(?:включи|выключи|поставь|заведи|запусти|отмени|убери|удали|останови|сделай|прибавь|убавь|продолжи|напомни|разбуди|засеки|сколько осталось|какие|что (?:сейчас )?играет|громче|потише|тише|пауза|дальше|следующ|таймер|будильник)`)
	aliceThings = regexp.MustCompile(`(таймер|будильник|напомни|напоминани|разбуди|засеки|музык|песн|трек|плейлист|радио|подкаст|сказк|звуки|громкост|громче|потише|тише|на паузу|пауза|играет|блютуз|bluetooth)`)
)

var (
	musicPlayRe  = regexp.MustCompile(`^(?:включи|поставь|заведи|запусти)(?:\s|$)`)
	musicWordsRe = regexp.MustCompile(`(музык|песн|трек|плейлист|радио|подкаст|альбом)`)
	notMusicRe   = regexp.MustCompile(`(таймер|будильник|напомни|напоминани|разбуди|засеки|громкост|громче|потише|тише|пауз|блютуз|bluetooth)`)
)

// AliceMusicRequest — просьба включить музыку (её Алиса включает из Яндекс Музыки), а не таймер или громкость.
func AliceMusicRequest(bare string) bool {
	return musicPlayRe.MatchString(bare) && musicWordsRe.MatchString(bare) && !notMusicRe.MatchString(bare)
}

// AliceSkillFor — как AliceSkill, но если Яндекс Музыка выключена, музыку Алисе не отдаём: Джарвис включит с YouTube.
func AliceSkillFor(text string, yandexMusic bool) (string, bool) {
	bare, ok := AliceSkill(text)
	if ok && !yandexMusic && AliceMusicRequest(bare) {
		return "", false
	}
	return bare, ok
}

// AliceSkill — просьба из тех, что выполняет только Алиса: вернуть её без имени, чтобы отдать Алисе.
func AliceSkill(text string) (string, bool) {
	bare := leadRe.ReplaceAllString(normText(text), "")
	if n := len(strings.Fields(bare)); n == 0 || n > 12 {
		return "", false
	}
	if bare == "дальше" || (aliceVerbs.MatchString(bare) && aliceThings.MatchString(bare)) {
		return bare, true
	}
	return "", false
}

// HomeCommand — короткая команда умного дома (или фраза сценария) — её можно отдать Алисе целиком.
func (t *Tools) HomeCommand(ctx context.Context, text string) (bool, string) {
	n := normText(text)
	bare := leadRe.ReplaceAllString(n, "")
	if bare == "" || len(strings.Fields(bare)) > 9 {
		return false, ""
	}
	if fastCmdRe.MatchString(n) || colorRe.MatchString(n) || (homeVerbs.MatchString(n+" ") && homeWords.MatchString(n)) {
		return true, bare
	}
	if c, err := t.Catalog(ctx); err == nil {
		for _, sc := range c.Scenarios {
			for _, ph := range append(scenarioPhrases(sc), sc.Name) {
				if p := normText(ph); p != "" && p == bare {
					return true, bare
				}
			}
		}
	}
	return false, ""
}
