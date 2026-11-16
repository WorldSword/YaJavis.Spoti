package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

func elevenCLI() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("укажи настройки")
	}
	store, err := OpenStore(os.Args[2])
	if err != nil {
		return err
	}
	cfg := store.Get()
	proxy := probeProxy(store)
	defer proxy.Close()
	if err := proxy.Apply(cfg.Vless); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	voices, err := elevenVoices(ctx, proxy.Client(25*time.Second), cfg.ElevenLabsKey)
	if err != nil {
		return err
	}
	for _, v := range voices {
		fmt.Printf("%s | %s | %s | %s\n", v.ID, v.Name, v.Category, v.Labels["language"])
	}
	var history struct {
		Items []struct {
			VoiceID   string `json:"voice_id"`
			VoiceName string `json:"voice_name"`
			Model     string `json:"model_id"`
		} `json:"history"`
	}
	if err := elevenGet(ctx, proxy.Client(20*time.Second), cfg.ElevenLabsKey, "/v1/history?page_size=5", &history); err == nil {
		for _, h := range history.Items {
			fmt.Printf("Недавно: %s | %s | %s\n", h.VoiceID, h.VoiceName, h.Model)
		}
	}
	return nil
}
