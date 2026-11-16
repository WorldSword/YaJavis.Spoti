package main

// Проверка ChatGPT по подписке на колонке, без динамика: N звонков подряд, в каждый — записанная фраза,
// как в настоящем разговоре (сначала весь оклик разом, потом «живой» микрофон с тишиной).
//   jarvisd probe-chatgpt-mic /tmp/phrase.wav 10
// По каждому звонку: за сколько открылся, услышала ли модель фразу, ответила ли голосом.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
)

func readWav16(path string) ([]int16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 44 || string(b[:4]) != "RIFF" {
		return nil, errors.New("нужен WAV 16 кГц моно 16 бит")
	}
	// ищем блок data
	i := 12
	for i+8 <= len(b) {
		id, n := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:i+8]))
		if id == "data" {
			end := min(len(b), i+8+n)
			pcm := make([]int16, (end-i-8)/2)
			for k := range pcm {
				pcm[k] = int16(binary.LittleEndian.Uint16(b[i+8+2*k:]))
			}
			return pcm, nil
		}
		i += 8 + n + n%2
	}
	return nil, errors.New("в WAV нет звука")
}

func runChatGPTMicProbe(args []string, dataDir string) {
	if len(args) < 1 {
		fmt.Println("использование: probe-chatgpt-mic <фраза.wav> [звонков] [задержка открытия мс]")
		return
	}
	phrase, err := readWav16(args[0])
	if err != nil {
		fmt.Println(err)
		return
	}
	n := 5
	if len(args) > 1 {
		n, _ = strconv.Atoi(args[1])
	}
	store, err := OpenStore(dataDir + "/settings.json")
	if err != nil {
		fmt.Println(err)
		return
	}
	st := store.Get()
	proxy := probeProxy(store)
	defer proxy.Close()
	proxy.use = func(svc string) bool { s := store.Get(); return s.UseVPN(svc) }
	if err := proxy.Apply(st.Vless); err != nil {
		fmt.Println("VPN:", err)
	}
	auth := func(context.Context) (ChatGPTAuth, error) { // токен продлевает работающий jarvisd
		s := store.Get()
		if !s.ChatGPT.SignedIn() || time.Until(time.Unix(s.ChatGPT.Expires, 0)) < time.Minute {
			return s.ChatGPT, errors.New("нет действующего входа в ChatGPT")
		}
		return s.ChatGPT, nil
	}
	p := st.ActiveProfile()
	ok := 0
	for call := 1; call <= n; call++ {
		t0 := time.Now()
		sess := NewChatGPTSession(context.Background(), auth, p, "Ты Джарвис. Отвечай одним коротким предложением.", proxy.LiveClientFor("chatgpt"),
			func(context.Context, string) (string, error) { return "Четыре.", nil }, &Journal{})
		sess.Feed(phrase) // как в разговоре: оклик целиком, пока звонок соединяется
		err := sess.Ready(15 * time.Second)
		open := time.Since(t0)
		heard, voice := false, 0
		if err == nil {
			stop := time.After(12 * time.Second)
			mic := time.NewTicker(10 * time.Millisecond)
			noise := make([]int16, MicRate/100)
		loop:
			for {
				select {
				case <-mic.C:
					for i := range noise {
						noise[i] = int16(rand.Intn(41) - 20) // тихий фон комнаты
					}
					sess.Feed(noise)
				case pcm, okc := <-sess.Audio():
					if !okc {
						break loop
					}
					if loud(pcm) {
						voice++
					}
				case <-stop:
					break loop
				}
				if voice > 25 && time.Since(t0) > 6*time.Second {
					break
				}
			}
			mic.Stop()
		}
		sess.statMu.Lock()
		ev := fmt.Sprint(sess.events)
		heard = sess.events["input_transcript.added"]+sess.events["input_transcript.delta"]+sess.events["session.input_transcript.delta"] > 0
		sent := float64(sess.sentLoud) / 50
		sess.statMu.Unlock()
		sess.mu.Lock()
		user := strings.TrimSpace(sess.turnUser)
		sess.mu.Unlock()
		sess.Close()
		res := "НЕ УСЛЫШАЛ"
		if heard && voice > 0 {
			res, ok = "ок", ok+1
		} else if heard {
			res = "услышал, но молчит"
		}
		fmt.Printf("звонок %d: %s | открыт за %.2f с (%v) | отправлено речи %.1f с | расшифровка «%s» | голос %d кусков | %s\n",
			call, res, open.Seconds(), err, sent, user, voice, ev)
		time.Sleep(3 * time.Second)
	}
	fmt.Printf("итог: %d из %d\n", ok, n)
}

// runChatGPTWhoProbe — спросить модель текстом «кто ты?» и показать ответ: проверка, слушает ли голос
// в подписке наши инструкции (вопрос — записанной фразой).
//
//	jarvisd probe-chatgpt-who "Кто ты? Как тебя зовут?"
func runChatGPTWhoProbe(args []string, dataDir string) {
	if len(args) < 1 {
		fmt.Println("использование: probe-chatgpt-who <вопрос.wav>")
		return
	}
	q := args[0]
	store, err := OpenStore(dataDir + "/settings.json")
	if err != nil {
		fmt.Println(err)
		return
	}
	st := store.Get()
	proxy := probeProxy(store)
	defer proxy.Close()
	proxy.use = func(svc string) bool { s := store.Get(); return s.UseVPN(svc) }
	if err := proxy.Apply(st.Vless); err != nil {
		fmt.Println("VPN:", err)
	}
	auth := func(context.Context) (ChatGPTAuth, error) {
		s := store.Get()
		if !s.ChatGPT.SignedIn() || time.Until(time.Unix(s.ChatGPT.Expires, 0)) < time.Minute {
			return s.ChatGPT, errors.New("нет действующего входа в ChatGPT")
		}
		return s.ChatGPT, nil
	}
	p := st.ActiveProfile()
	instr := st.PromptText(p.Prompt) + identityRules
	sess := NewChatGPTSession(context.Background(), auth, p, instr, proxy.LiveClientFor("chatgpt"),
		func(context.Context, string) (string, error) { return "Готово.", nil }, &Journal{})
	defer sess.Close()
	if err := sess.Ready(15 * time.Second); err != nil {
		fmt.Println("звонок:", err)
		return
	}
	phrase, err := readWav16(q)
	if err != nil {
		fmt.Println(err)
		return
	}
	sess.Feed(phrase)
	go func() {
		noise := make([]int16, MicRate/100)
		for i := 0; i < 1200 && sess.ctx.Err() == nil; i++ {
			time.Sleep(10 * time.Millisecond)
			sess.Feed(noise)
		}
	}()
	answer := ""
	var sentAt, voiceAt time.Time
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case pcm := <-sess.Audio():
			if loud(pcm) && voiceAt.IsZero() {
				voiceAt = time.Now()
			}
		case <-time.After(20 * time.Millisecond):
		}
		sess.pendingMu.Lock()
		if sentAt.IsZero() && len(sess.pending) < 4*(MicRate/50) {
			sentAt = time.Now() // фраза ушла целиком — дальше в очереди только тишина
		}
		sess.pendingMu.Unlock()
		sess.mu.Lock()
		if b := strings.TrimSpace(sess.turnBot); len(b) > len(answer) {
			answer = b
		}
		sess.mu.Unlock()
	}
	sess.statMu.Lock()
	ev := fmt.Sprint(sess.events)
	sess.statMu.Unlock()
	lat := "голоса не было"
	if !voiceAt.IsZero() && !sentAt.IsZero() {
		lat = fmt.Sprintf("%.2f с от конца фразы до голоса", voiceAt.Sub(sentAt).Seconds())
	}
	fmt.Printf("звонок — %s\nответ: %s\nсобытия: %s\n", lat, answer, ev)
}
