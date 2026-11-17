package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Разговор с поддельным сервером Gemini: проверяем, что клиент правильно здоровается, шлёт микрофон,
// принимает расшифровку и голос, отвечает на вызов инструмента. Настоящего Gemini тест не трогает.
func TestGeminiSessionProtocol(t *testing.T) {
	type seen struct {
		setup    map[string]any
		key      string
		audio    bool
		toolResp map[string]any
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ws.SetReadLimit(1 << 20)
		ctx := r.Context()
		var s seen
		s.key = r.Header.Get("x-goog-api-key")
		read := func() map[string]any {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return nil
			}
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			return m
		}
		write := func(v any) {
			b, _ := json.Marshal(v)
			_ = ws.Write(ctx, websocket.MessageBinary, b) // Gemini шлёт JSON двоичными кадрами
		}
		first := read()
		s.setup, _ = first["setup"].(map[string]any)
		write(map[string]any{"setupComplete": map[string]any{}})
		for m := read(); m != nil; m = read() {
			if ri, ok := m["realtimeInput"].(map[string]any); ok && !s.audio {
				a, _ := ri["audio"].(map[string]any)
				s.audio = a["mimeType"] == "audio/pcm;rate=16000" && a["data"] != ""
				write(map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": "Джарвис, включи свет"}}})
				write(map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "call-1", "name": "smart_home",
					"args": map[string]any{"device_ids": []any{"lamp"}, "on": true, "modes": []any{"work_speed=turbo"}}}}}})
			}
			if tr, ok := m["toolResponse"].(map[string]any); ok {
				s.toolResp = tr
				pcm := make([]byte, 2*geminiRate) // секунда громкого звука
				for i := 0; i < len(pcm); i += 2 {
					pcm[i+1] = 0x20
				}
				write(map[string]any{"serverContent": map[string]any{
					"modelTurn":           map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(pcm)}}}},
					"outputTranscription": map[string]any{"text": "Готово, свет включён"}}})
				got <- s
			}
		}
	}))
	defer srv.Close()
	old := geminiURL
	geminiURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	defer func() { geminiURL = old }()

	j := &Journal{}
	p := Profile{Engine: "gemini", Voice: "kore", WebSearch: true}
	sess := NewGeminiSession(context.Background(), "test-key", p, "Ты Джарвис.", NewTools(func() string { return "" }, DirectClient), http.DefaultClient, j)
	defer sess.Close()
	if err := sess.Ready(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	sess.Feed(make([]int16, 1600))

	var s seen
	select {
	case s = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не дождался ответа на вызов инструмента")
	}
	if s.key != "test-key" {
		t.Errorf("ключ должен идти в заголовке x-goog-api-key, пришло %q", s.key)
	}
	if s.setup["model"] != "models/gemini-3.8-live" {
		t.Errorf("модель в приветствии: %v", s.setup["model"])
	}
	gen, _ := s.setup["generationConfig"].(map[string]any)
	b, _ := json.Marshal(gen)
	if !strings.Contains(string(b), `"voiceName":"Kore"`) || !strings.Contains(string(b), `"AUDIO"`) {
		t.Errorf("голос и звук в приветствии: %s", b)
	}
	if _, ok := s.setup["inputAudioTranscription"]; !ok {
		t.Error("нужна расшифровка речи человека — по ней проверяется имя и ведётся переписка")
	}
	language, _ := json.Marshal(s.setup["inputAudioTranscription"])
	if !strings.Contains(string(language), "ru-RU") {
		t.Fatal("Russian transcription hint missing")
	}
	tb, _ := json.Marshal(s.setup["tools"])
	if !strings.Contains(string(tb), "googleSearch") {
		t.Errorf("поиск в интернете включён у ассистента, а в инструментах его нет: %s", tb)
	}
	if !s.audio {
		t.Error("микрофон должен уходить как audio/pcm;rate=16000")
	}
	rb, _ := json.Marshal(s.toolResp)
	if !strings.Contains(string(rb), `"id":"call-1"`) || !strings.Contains(string(rb), `"result"`) {
		t.Errorf("ответ на вызов инструмента: %s", rb)
	}

	samples := 0
	deadline := time.After(3 * time.Second)
	for samples < geminiRate {
		select {
		case pcm, ok := <-sess.Audio():
			if !ok {
				t.Fatal("голос оборвался")
			}
			samples += len(pcm)
		case <-deadline:
			t.Fatalf("голос модели не дошёл целиком: %d из %d отсчётов", samples, geminiRate)
		}
	}
	time.Sleep(50 * time.Millisecond)
	items := j.Items(5)
	if len(items) != 1 || items[0].Text != "Джарвис, включи свет" || items[0].Answer != "Готово, свет включён" || items[0].Route != "Gemini Live" {
		t.Errorf("переписка: %+v", items)
	}
	if id, text, _ := sess.Utterance(); id == 0 || text != "Джарвис, включи свет" {
		t.Errorf("реплика для проверки имени: %d %q", id, text)
	}
}

func TestGeminiHomeArgs(t *testing.T) {
	got := geminiHomeArgs(map[string]any{"device_ids": []any{"a"}, "on": true,
		"modes": []any{"work_speed=turbo"}, "toggles": []any{"pause=true"}, "ranges": []any{"temperature=22"}})
	var a struct {
		DeviceIDs []string `json:"device_ids"`
		On        *bool
		Modes     map[string]string
		Toggles   map[string]bool
		Ranges    map[string]float64
	}
	if err := json.Unmarshal([]byte(got), &a); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if len(a.DeviceIDs) != 1 || a.On == nil || !*a.On || a.Modes["work_speed"] != "turbo" || !a.Toggles["pause"] || a.Ranges["temperature"] != 22 {
		t.Errorf("аргументы умного дома разобраны неверно: %s → %+v", got, a)
	}
}
