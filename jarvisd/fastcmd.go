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
	if t.token() != "" {
		var matched bool
		if t.yandexCall(ctx, "scenario-match", bare, &matched) == nil && matched {
			return true, bare
		}
	}
	return false, ""
}
