package main

// Сброс пароля веб-настроек кнопками колонки — если пароль забыли.
// Последовательность: «громче», «тише» поочерёдно 8 раз подряд (+ − + − + − + −), между нажатиями
// не дольше 1,5 с. Случайно так не нажать, а громкость в итоге остаётся прежней.
// Кнопки читаем из /dev/input без захвата — Алиса их тоже видит, как обычно.

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	keyVolumeDown = 114
	keyVolumeUp   = 115
	resetPresses  = 8
)

// WatchButtons — следить за кнопками всех устройств ввода; onReset — набрана последовательность сброса.
func WatchButtons(onReset func()) {
	devs, _ := filepath.Glob("/dev/input/event*")
	keys := make(chan uint16, 16)
	for _, d := range devs {
		go readKeys(d, keys)
	}
	var seq []uint16
	var last time.Time
	for k := range keys {
		if k != keyVolumeUp && k != keyVolumeDown {
			seq = nil
			continue
		}
		if time.Since(last) > 1500*time.Millisecond || (len(seq) > 0 && seq[len(seq)-1] == k) {
			seq = seq[:0]
		}
		if len(seq) == 0 && k != keyVolumeUp { // начинается с «громче»
			last = time.Now()
			continue
		}
		seq, last = append(seq, k), time.Now()
		if len(seq) >= resetPresses {
			seq = nil
			onReset()
		}
	}
}

// readKeys — нажатия (EV_KEY, value=1) из одного /dev/input/eventN.
func readKeys(path string, out chan<- uint16) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	// struct input_event: timeval (2×long) + type u16 + code u16 + value s32
	size := 16 + 8
	if strconv.IntSize == 32 {
		size = 8 + 8
	}
	buf := make([]byte, size*16)
	for {
		n, err := f.Read(buf)
		if err != nil {
			log.Printf("кнопки: %s: %v", path, err)
			return
		}
		for i := 0; i+size <= n; i += size {
			ev := buf[i+size-8 : i+size]
			typ, code := binary.LittleEndian.Uint16(ev[0:]), binary.LittleEndian.Uint16(ev[2:])
			val := int32(binary.LittleEndian.Uint32(ev[4:]))
			if typ == 1 && val == 1 { // EV_KEY, нажатие
				select {
				case out <- code:
				default:
				}
			}
		}
	}
}

// resetPassword — пароль снят, все входы недействительны; новый можно задать в течение 30 минут.
func (w *Web) resetPassword() {
	_ = w.store.Update(func(s *Settings) error {
		s.PasswordHash = ""
		s.SessionKey = randHex(32)
		return nil
	})
	w.mu.Lock()
	w.resetAt = time.Now()
	w.mu.Unlock()
	log.Printf("веб-настройки: пароль сброшен кнопками колонки")
	go w.engine.leds.Show(LedThinking, "#00b4ff", 70, 3*time.Second)
	go w.engine.systemNotice(resetChime(), "Пароль веб-настроек сброшен. Откройте настройки и задайте новый в течение получаса.",
		"Повтори за мной: пароль Джарвиса сброшен, задайте новый в настройках")
}

// resetChime — сигнал «пароль сброшен»: три восходящие ноты дважды, не похоже на короткие сигналы быстрых команд.
// Собирается в памяти и играет на колонке без сети, ключей и VPN — подтверждение есть даже при первой настройке.
func resetChime() []int16 {
	var out []int16
	for rep := 0; rep < 2; rep++ {
		for _, f := range []float64{660, 880, 1320} {
			n := int(0.11 * MicRate)
			for i := 0; i < n; i++ {
				env := math.Min(1, math.Min(float64(i), float64(n-i))/(0.008*MicRate))
				out = append(out, int16(9000*env*math.Sin(2*math.Pi*f*float64(i)/MicRate)))
			}
			out = append(out, make([]int16, MicRate/40)...)
		}
		out = append(out, make([]int16, MicRate/6)...)
	}
	return out
}

// systemNotice — системное сообщение колонки: сразу локальный сигнал, затем, если получится, голосом —
// активным ассистентом (не дольше 8 с, без долгих повторов), а если он не настроен или не ответил —
// штатной Алисой (aliceText). Голос — только дополнение: сигнал звучит всегда.
func (e *Engine) systemNotice(chime []int16, text, aliceText string) {
	if e.player.Start(MicRate) == nil {
		_ = e.player.Write(make([]int16, MicRate/4)) // тракт просыпается
		_ = e.player.Write(chime)
		e.player.Finish(4 * time.Second)
	}
	if e.notice != nil {
		e.notice(context.Background(), text)
		return
	}
	s := e.store.Get()
	p := s.ActiveProfile()
	if e.proxy != nil && keyMissing(s, p.Engine) == "" {
		voicePrompt := ""
		for _, v := range s.LiveVoices {
			if v.ID == p.Voice {
				voicePrompt = v.Prompt
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_, err := e.sayAloudContext(ctx, s, p, voicePrompt, text, 8*time.Second)
		cancel()
		if err == nil || errors.Is(err, errVoiceInterrupted) {
			return
		}
		log.Printf("системное сообщение: ассистент не озвучил (%v) — прошу Алису", err)
	}
	if aliceText != "" {
		if err := AliceText(aliceText, false); err != nil {
			log.Printf("системное сообщение: Алиса недоступна: %v", err)
		}
	}
}
