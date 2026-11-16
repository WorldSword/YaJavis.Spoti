package main

// Настройки общей памяти: запоминать ли разговоры и кто сжимает их в конспект (провайдер и модель).

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"
)

type MemorySettings struct {
	Off      bool   `json:"off,omitempty"`      // не запоминать разговоры: каждый звонок с чистого листа
	Provider string `json:"provider,omitempty"` // кто делает конспект: "" — сам выбирает; chatgpt | gemini | openai | none
	Model    string `json:"model,omitempty"`    // "" — модель по умолчанию у провайдера
}

// memoryProviders — порядок автовыбора: сначала бесплатное (подписка, бесплатный уровень Gemini), потом платный ключ.
var memoryProviders = []string{"chatgpt", "gemini", "openai"}

var errNoSummarizer = errors.New("конспект не делается: память выключена или нет ни подписки ChatGPT, ни ключей Gemini и OpenAI")

func memoryHas(s Settings, provider string) bool {
	switch provider {
	case "chatgpt":
		return s.ChatGPT.SignedIn()
	case "gemini":
		return s.GeminiKey != ""
	case "openai":
		return s.OpenAIKey != ""
	}
	return false
}

// memoryModels — модели провайдера для конспекта; "" у Gemini — по очереди, при лимите следующая.
func memoryModels(provider string) []string {
	if provider == "gemini" {
		return append([]string{""}, geminiTextModels...)
	}
	return BackendModels
}

type memoryStep struct{ Provider, Model string }

// memoryChain — кто делает конспект: выбранный провайдер или все доступные по очереди (если один не ответил — следующий).
func memoryChain(s Settings) []memoryStep {
	m := s.Memory
	if m.Off || m.Provider == "none" {
		return nil
	}
	model := func(p string) string {
		if slices.Contains(memoryModels(p), m.Model) {
			return m.Model
		}
		return memoryModels(p)[0]
	}
	if memoryHas(s, m.Provider) {
		return []memoryStep{{m.Provider, model(m.Provider)}}
	}
	var out []memoryStep // «сам выбирает» или выбранного больше нет (вышли из ChatGPT, убрали ключ)
	for _, p := range memoryProviders {
		if memoryHas(s, p) {
			out = append(out, memoryStep{p, model(p)})
		}
	}
	return out
}

// summarize — сжать реплики в конспект текстовой моделью без инструментов.
func (e *Engine) summarize(ctx context.Context, instructions, text string) (string, error) {
	s := e.store.Get()
	chain := memoryChain(s)
	if len(chain) == 0 {
		return "", errNoSummarizer
	}
	var err error
	for i, st := range chain {
		var answer string
		if answer, err = e.askPlain(ctx, s, st, instructions, text); err == nil {
			return answer, nil
		}
		if ctx.Err() != nil {
			break
		}
		if i+1 < len(chain) {
			log.Printf("память: %s не ответил (%v) — пробую %s", st.Provider, shortErr(err), chain[i+1].Provider)
		}
	}
	return "", err
}

func (e *Engine) askPlain(ctx context.Context, s Settings, st memoryStep, instructions, text string) (string, error) {
	p := Profile{BackendModel: st.Model, Effort: "low"}
	switch st.Provider {
	case "openai":
		return askOpenAI(ctx, e.proxy.ClientFor("openai", 90*time.Second), s.OpenAIKey, p, instructions, text, nil)
	case "chatgpt":
		a, err := e.chatgptAuth(ctx)
		if err != nil {
			return "", err
		}
		return askCodex(ctx, e.proxy.ClientFor("chatgpt", 90*time.Second), a, p, instructions, text, nil)
	case "gemini":
		models := geminiTextModels
		if st.Model != "" {
			models = []string{st.Model}
		}
		return askGeminiModels(ctx, e.proxy.ClientFor("gemini", 90*time.Second), s.GeminiKey, instructions, text, false, models)
	}
	return "", errNoSummarizer
}

// memoryState — для панели: настройки, кто сейчас делает конспект и что можно выбрать.
func memoryState(s Settings) map[string]any {
	has := map[string]bool{}
	models := map[string][]string{}
	for _, p := range memoryProviders {
		has[p] = memoryHas(s, p)
		models[p] = memoryModels(p)
	}
	st := map[string]any{"off": s.Memory.Off, "provider": s.Memory.Provider, "model": s.Memory.Model, "has": has, "models": models}
	if chain := memoryChain(s); len(chain) > 0 {
		st["active"], st["active_model"] = chain[0].Provider, chain[0].Model
	}
	return st
}
