package main

// Голос и Spotify: «Джарвис, включи … в Spotify» и управление тем, что играет в Soloist.

import (
	"context"
	"log"
	"strings"
	"time"
	"unicode"
)

// aliceTextFunc — команда штатной Алисе; в тестах подменяется.
var aliceTextFunc = AliceText

// playSpotifyContext — включить в Spotify; ответ — что сказать вслух и что передать модели.
func (e *Engine) playSpotifyContext(ctx context.Context, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "Что включить в Spotify?"
	}
	if err := e.spotify.Play(ctx, query); err != nil {
		log.Printf("spotify: «%s»: %v", query, err)
		return "Не получилось включить: " + shortErr(err)
	}
	return "Включаю в Spotify: " + query + "."
}

// spotifyOwns — команды управления (пауза, «продолжи», кнопки панели) относятся к Spotify:
// он играет сейчас, либо стоит на паузе и больше никакой музыки (YouTube, Алиса) нет.
func (e *Engine) spotifyOwns() bool {
	sp := e.spotify
	if sp.Playing() {
		return true
	}
	return sp.Active() && !e.music.Active() && !e.alice.Active()
}

// pauseSpotifyIfPlaying — зазвучала другая музыка: Spotify на паузу, чтобы не играли вместе.
func (e *Engine) pauseSpotifyIfPlaying() {
	if !e.spotify.Playing() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := e.spotify.Control(ctx, "pause"); err != nil {
			log.Printf("spotify: пауза при запуске другой музыки: %v", err)
		}
	}()
}

// controlSpotify — «пауза», «продолжи», «следующее», «выключи», «что играет» для Spotify, пока он активен.
func (e *Engine) controlSpotify(ctx context.Context, text string) (string, bool) {
	sp := e.spotify
	if !e.spotifyOwns() {
		return "", false
	}
	action, _, _, ok := musicCommand(text)
	if !ok {
		return "", false
	}
	answer, arg := "", ""
	switch action {
	case "what":
		t := sp.Snapshot().Track
		answer = "Играет «" + t.Title + "»"
		if sp.Paused() {
			answer = "На паузе «" + t.Title + "»"
		}
		if t.Author != "" {
			answer += " — " + t.Author
		}
		return answer + ".", true
	case "pause":
		answer, arg = "Пауза.", "pause"
	case "resume":
		answer, arg = "Продолжаю.", "resume"
	case "next":
		answer, arg = "Включаю следующее.", "next"
	case "stop":
		answer, arg = "Выключил.", "pause" // у Soloist «стоп» — пауза: stop.sh гасит сам сервис
	default:
		return "", false // перемотка и радио у Soloist недоступны — команду разберёт остальной код
	}
	if err := sp.Control(ctx, arg); err != nil {
		log.Printf("spotify: «%s»: %v", text, err)
		return "Не получилось: " + shortErr(err), true
	}
	_, _ = sp.Now(ctx) // панель сразу видит новое состояние
	log.Printf("spotify: «%s» → %s", text, answer)
	return answer, true
}

// spotifyStarted — зазвучал Spotify: YouTube Джарвиса останавливаем, у Алисы играла музыка — «пауза».
func (e *Engine) spotifyStarted() {
	if e.music != nil && e.music.Playing() {
		e.music.Stop()
		log.Printf("spotify: зазвучал Spotify — YouTube остановлен")
	}
	if e.alice.Playing() {
		if err := aliceTextFunc("пауза", true); err == nil {
			log.Printf("spotify: у Алисы играла своя музыка — поставил её на паузу")
		}
	}
}

// spotifyContextRequest — «включи в Spotify мои любимые треки / диджея / радио по этому треку».
// Нужно слово Spotify; kind: liked | dj | radio.
func spotifyContextRequest(text string) (string, bool) {
	if !spotifyRe.MatchString(text) {
		return "", false
	}
	return contextKind(text)
}

// spotifyContextRequestOwned — то же, но слово Spotify не нужно: Spotify уже играет и речь о нём.
func spotifyContextRequestOwned(text string) (string, bool) { return contextKind(text) }

func contextKind(text string) (string, bool) {
	t := strings.ToLower(text)
	verb := false
	for _, v := range []string{"включи", "поставь", "запусти", "сыграй", "играй", "вруби", "давай"} {
		if strings.Contains(t, v) {
			verb = true
		}
	}
	if !verb {
		return "", false
	}
	words := strings.FieldsFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	has := func(prefixes ...string) bool {
		for _, w := range words {
			for _, p := range prefixes {
				if strings.HasPrefix(w, p) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("любим", "лайк", "избранн", "понравивш", "liked"):
		return "liked", true
	case has("радио") && has("этому", "текущ", "трек", "песн", "этой"):
		return "radio", true
	case has("диджей", "диджея", "dj") || strings.Contains(t, "ди-джей") || strings.Contains(t, "ди джей"):
		return "dj", true
	}
	return "", false
}

// playSpotifyKind — голосовая команда или кнопка панели: ответ вслух.
func (e *Engine) playSpotifyKind(ctx context.Context, kind string) string {
	var err error
	say := ""
	switch kind {
	case "liked":
		err, say = e.spotify.PlayLiked(ctx), "Включаю ваши любимые треки в Spotify."
	case "dj":
		err, say = e.spotify.PlayDJ(ctx), "Включаю DJ в Spotify."
	case "radio":
		err, say = e.spotify.PlayRadio(ctx), "Включаю радио по этому треку."
	default:
		return "Не поняла, что включить в Spotify."
	}
	if err != nil {
		log.Printf("spotify: %s: %v", kind, err)
		return "Не получилось: " + shortErr(err)
	}
	_, _ = e.spotify.Now(ctx)
	return say
}

// spotifyKind — команда про любимые треки, DJ или радио, адресованная Spotify.
func (e *Engine) spotifyKind(text string) (string, bool) {
	if e.spotify == nil || !e.spotify.Installed() {
		return "", false
	}
	if kind, ok := spotifyContextRequest(text); ok {
		return kind, true
	}
	if e.spotifyOwns() {
		return spotifyContextRequestOwned(text)
	}
	return "", false
}
