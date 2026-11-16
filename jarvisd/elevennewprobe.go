package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

func probeElevenNew() error {
	store, err := OpenStore(os.Args[2])
	if err != nil {
		return err
	}
	cfg := store.Get()
	p := cfg.ActiveProfile()
	proxy := probeProxy(store)
	defer proxy.Close()
	if err = proxy.Apply(cfg.Vless); err != nil {
		return err
	}
	client := proxy.Client(40 * time.Second)
	for _, model := range []string{"eleven_v4_turbo", "eleven_v4", "eleven_v3_conversational"} {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		p.TTSModel = model
		n := 0
		err := elevenSpeak(ctx, client, cfg.ElevenLabsKey, p, "Привет. Всё готово.", func(pcm []int16) error { n += len(pcm); return nil })
		cancel()
		if err != nil {
			fmt.Printf("%s: ERROR %v\n", model, err)
		} else {
			fmt.Printf("%s: получено %.1f с PCM без динамика\n", model, float64(n)/elevenRate)
		}
	}
	for _, model := range elevenRealtimeModels {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		p.LiveModel = model
		s := NewElevenRealtimeSession(ctx, cfg.ElevenLabsKey, p, "Отвечай по-русски кратко. Это проверка без инструментов.", NewTools(func() string { return "" }, DirectClient), client, &Journal{})
		err := s.Ready(15 * time.Second)
		if err != nil {
			fmt.Printf("%s: ERROR %v\n", model, err)
			s.Close()
			cancel()
			continue
		}
		err = s.Say("Привет. Всё готово.")
		n := 0
		last := time.Now()
		tick := time.NewTicker(100 * time.Millisecond)
	loop:
		for err == nil {
			select {
			case pcm, ok := <-s.Audio():
				if !ok {
					err = fmt.Errorf("соединение закрыто")
					break loop
				}
				n += len(pcm)
				last = time.Now()
			case <-tick.C:
				if n > 0 && time.Since(last) > time.Second {
					break loop
				}
			case <-ctx.Done():
				err = ctx.Err()
				break loop
			}
		}
		tick.Stop()
		s.Close()
		cancel()
		if err != nil {
			fmt.Printf("%s: ERROR %v\n", model, err)
		} else {
			fmt.Printf("%s: получено %.1f с голосового ответа без динамика\n", model, float64(n)/elevenRate)
		}
	}
	return nil
}
