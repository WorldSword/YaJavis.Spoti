package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type ElevenModelInfo struct {
	ID   string `json:"model_id"`
	Name string `json:"name"`
	TTS  bool   `json:"can_do_text_to_speech"`
}

// Read model metadata and validate Realtime session configuration without audio.
func probeElevenModels() error {
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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	client := proxy.Client(20 * time.Second)
	var models []ElevenModelInfo
	if err := elevenGet(ctx, client, cfg.ElevenLabsKey, "/v1/models", &models); err != nil {
		fmt.Printf("Каталог моделей: %v\n", err)
	}
	for _, m := range models {
		if strings.HasPrefix(m.ID, "eleven_") || strings.HasPrefix(m.ID, "scribe_") {
			fmt.Printf("Модель: %s | %s | TTS=%v\n", m.ID, m.Name, m.TTS)
		}
	}
	for _, model := range []string{"eleven_realtime_v1_mini", "eleven_realtime_v1_max"} {
		body := map[string]any{"session": map[string]any{"type": "realtime", "model": model, "instructions": "Отвечай по-русски.", "output_modalities": []string{"audio"}, "max_duration_seconds": 60}, "expires_after": map[string]any{"seconds": 60}}
		b, _ := json.Marshal(body)
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, elevenBase+"/v1/convai/realtime/client_secrets", bytes.NewReader(b))
		r.Header.Set("xi-api-key", cfg.ElevenLabsKey)
		r.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(r)
		if err != nil {
			fmt.Printf("Realtime %s: %v\n", model, err)
			continue
		}
		if resp.StatusCode/100 != 2 {
			err = elevenError(resp, cfg.ElevenLabsKey)
			resp.Body.Close()
			fmt.Printf("Realtime %s: %v\n", model, err)
			continue
		}
		var result struct {
			Value   string `json:"value"`
			Session struct {
				Model string `json:"model"`
			} `json:"session"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil || result.Value == "" {
			fmt.Printf("Realtime %s: не получено подтверждение конфигурации\n", model)
			continue
		}
		fmt.Printf("Realtime %s: конфигурация принята (ответ модели %s); звук не отправлялся, разговор не запускался.\n", model, result.Session.Model)
	}
	return nil
}
