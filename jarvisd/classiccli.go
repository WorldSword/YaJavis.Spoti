package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func configureClassic() error {
	if len(os.Args) < 4 {
		return errors.New("укажи настройки и ID голоса ElevenLabs")
	}
	store, err := OpenStore(os.Args[2])
	if err != nil {
		return err
	}
	cfg := store.Get()
	if msg := keyMissing(cfg, "elevenlabs"); msg != "" {
		return errors.New(msg)
	}
	proxy := probeProxy(store)
	defer proxy.Close()
	if err = proxy.Apply(cfg.Vless); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	voices, err := elevenVoices(ctx, proxy.Client(20*time.Second), cfg.ElevenLabsKey)
	if err != nil {
		return err
	}
	voiceName := ""
	for _, v := range voices {
		if v.ID == os.Args[3] {
			voiceName = v.Name
			break
		}
	}
	if voiceName == "" {
		return errors.New("этот голос не найден в аккаунте ElevenLabs")
	}
	var chosen Profile
	err = store.Update(func(s *Settings) error {
		p := s.Profiles[0]
		index := -1
		for i, x := range s.Profiles {
			if isClassic(x) {
				p = x
				index = i
				break
			}
		}
		if index < 0 {
			p.ID, p.Name, p.Color = "p"+randHex(4), "ElevenLabs", 4
		}
		p.TTSVoice, p.TTSModel = os.Args[3], elevenDefaultModel
		classicDefaults(&p)
		p.fillDefaults(s)
		chosen = p
		if index < 0 {
			s.Profiles = append(s.Profiles, p)
		} else {
			s.Profiles[index] = p
		}
		s.Active = p.ID
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Активен классический ElevenLabs: %s → %s → %s (%s).\n", chosen.STTEngine, chosen.LLMModel, voiceName, chosen.TTSModel)
	return nil
}

// Checks all three real services while keeping both the speaker and home tools disabled.
func probeClassic() error {
	if len(os.Args) < 4 {
		return errors.New("укажи настройки и ID голоса ElevenLabs")
	}
	store, err := OpenStore(os.Args[2])
	if err != nil {
		return err
	}
	cfg := store.Get()
	p := cfg.ActiveProfile()
	p.TTSVoice = os.Args[3]
	if len(os.Args) > 4 {
		p.TTSModel = os.Args[4]
	}
	if len(os.Args) > 5 {
		p.STTModel = os.Args[5]
	}
	classicDefaults(&p)
	p.WebSearch, p.Priority = false, false
	proxy := probeProxy(store)
	defer proxy.Close()
	if err = proxy.Apply(cfg.Vless); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	client := proxy.Client(50 * time.Second)
	// Generate a Russian microphone phrase with the selected voice, then feed it to Scribe.
	var source []int16
	if err = elevenSpeak(ctx, client, cfg.ElevenLabsKey, p, "Джарвис, скажи привет.", func(pcm []int16) error { source = append(source, pcm...); return nil }); err != nil {
		return err
	}
	fmt.Printf("ElevenLabs TTS: получено %.1f с звука (%s), динамик не включался.\n", float64(len(source))/elevenRate, func() string { var m levelMeter; m.add(source, elevenRate); return m.String() }())
	noTools := NewTools(func() string { return "" }, DirectClient)
	brain := func(ctx context.Context, text string) (string, error) {
		return askOpenAI(ctx, client, cfg.OpenAIKey, p, "Ты Джарвис. Отвечай по-русски одной короткой фразой. Не используй инструменты.", text, noTools)
	}
	s := NewClassicSession(ctx, cfg.ElevenLabsKey, p, brain, client, &Journal{})
	defer s.Close()
	if err = s.Ready(15 * time.Second); err != nil {
		return err
	}
	mic := make([]int16, len(source)*2/3)
	for i := range mic {
		at := i * 3 / 2
		next := min(at+1, len(source)-1)
		v := float64(source[at])
		if i%2 != 0 {
			v = (v + float64(source[next])) / 2
		}
		mic[i] = int16(v / 5.6)
	}
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for i := 0; i < len(mic); i += 1600 {
			s.Feed(mic[i:min(i+1600, len(mic))])
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	samples := 0
	last := time.Now()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case pcm, ok := <-s.Audio():
			if !ok {
				return errors.New("классическая сессия закрылась до ответа")
			}
			samples += len(pcm)
			last = time.Now()
		case <-tick.C:
			if samples > elevenRate/2 && time.Since(last) > 1200*time.Millisecond {
				_, text, _ := s.Utterance()
				s.dialog.mu.Lock()
				answer := strings.TrimSpace(s.dialog.turnBot)
				s.dialog.mu.Unlock()
				if strings.TrimSpace(text) == "" || answer == "" {
					return errors.New("нет расшифровки или ответа текстовой модели")
				}
				fmt.Printf("Scribe: %s\nТекстовая модель %s: %s\nОтвет ElevenLabs: %.1f с PCM. Вся классическая цепочка работает; динамик и устройства дома не использовались.\n", text, p.LLMModel, answer, float64(samples)/elevenRate)
				return nil
			}
		case <-ctx.Done():
			return errors.New("классический режим не завершил проверку: " + ctx.Err().Error())
		}
	}
}
