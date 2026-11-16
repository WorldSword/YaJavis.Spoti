package main

// Голос и Spotify: «Джарвис, включи … в Spotify» и управление тем, что играет в Soloist.

import (
	"context"
	"log"
	"strings"
	"time"
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
