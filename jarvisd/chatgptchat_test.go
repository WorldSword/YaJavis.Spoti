package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.json")
	c := LoadChatThread(path)
	id := c.Thread()
	if id == "" {
		t.Fatal("нет номера беседы")
	}
	for i := 0; i < 60; i++ {
		c.Add("вопрос", strings.Repeat("о", 300))
	}
	c2 := LoadChatThread(path) // после перезапуска — тот же чат и весь архив
	if c2.Thread() != id || len(c2.archive) != 60 || c2.Next != 61 {
		t.Fatalf("после перезапуска: %s %d %d", c2.Thread(), len(c2.archive), c2.Next)
	}
	r := c2.Recent(1000)
	if len(r) == 0 || len(r) > 4 || r[len(r)-1] != c2.archive[59] {
		t.Fatalf("Recent(1000) = %d реплик", len(r))
	}
	c2.Reset()
	if c2.Thread() == id || len(c2.Recent(chatKeepChars)) != 0 || c2.Context(chatKeepChars) != "" {
		t.Fatal("новый чат не начался")
	}
	if !c2.HasArchive() {
		t.Fatal("после «Начать новый чат» архив должен остаться")
	}
	c2.Add("новое", "")
	if c3 := LoadChatThread(path); len(c3.Recent(chatKeepChars)) != 1 || len(c3.archive) != 61 {
		t.Fatalf("после перезапуска новый чат: %d, архив %d", len(c3.Recent(chatKeepChars)), len(c3.archive))
	}
	c2.Erase()
	if c4 := LoadChatThread(path); c4.HasArchive() || len(c4.Recent(chatKeepChars)) != 0 {
		t.Fatal("«Стереть память» должна стереть и архив")
	}
	var j Journal
	got := 0
	j.OnRemember = func(t Turn) { got++ }
	j.note("x", "привет", "здравствуй")
	if got != 1 {
		t.Fatal("журнал не сообщил о реплике")
	}
}

// Старый chatgpt-chat.json (реплики прямо в файле, без номеров) переезжает в архив.
func TestChatThreadMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatgpt-chat.json")
	old, _ := json.Marshal(map[string]any{"id": "abc", "since": 1, "turns": []ChatTurn{{TS: 100, User: "привет"}, {TS: 200, Bot: "здравствуй"}}})
	_ = os.WriteFile(path, old, 0o600)
	c := LoadChatThread(path)
	if c.Thread() != "abc" || len(c.Recent(chatKeepChars)) != 2 || c.archive[1].N != 2 {
		t.Fatalf("переезд: %+v", c.archive)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "chatgpt-chat-archive.jsonl")); err != nil {
		t.Fatal("архив не записан")
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), `"turns"`) {
		t.Fatal("реплики должны уйти из файла чата в архив")
	}
}

func TestChatCompact(t *testing.T) {
	c := LoadChatThread(filepath.Join(t.TempDir(), "chat.json"))
	clock := time.Date(2026, 11, 14, 18, 5, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return clock })
	for i := 0; i < 40; i++ {
		c.Add("расскажи про рецепт пиццы номер "+string(rune('А'+i%30)), strings.Repeat("тесто ", 50))
	}
	clock = clock.Add(48 * time.Hour)
	var gotText string
	c.SetSummarizer(func(ctx context.Context, instr, text string) (string, error) {
		gotText = text
		return "Хронология: 14.11 хозяин спрашивал про пиццу, Джарвис рассказал рецепт теста.", nil
	})
	if err := c.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotText, "суббота 14 ноября") || !strings.Contains(gotText, "18:05 Человек: расскажи") || strings.Contains(gotText, "позавчера") {
		t.Fatalf("в конспект должны уйти реплики с абсолютной датой, временем и тем, кто сказал:\n%.300s", gotText)
	}
	if c.SummaryUpto == 0 || chatSize(c.pendingLocked()) > chatCompactKeep+400 {
		t.Fatalf("после сжатия дословно осталось %d букв", chatSize(c.pendingLocked()))
	}
	ctx := c.Context(chatKeepChars)
	if !strings.Contains(ctx, "Конспект") || !strings.Contains(ctx, "14.11 хозяин") || !strings.Contains(ctx, "позавчера, суббота 14 ноября") {
		t.Fatalf("память для звонка:\n%.500s", ctx)
	}
	// второй проход: прежний конспект уходит в модель вместе с новыми репликами
	for i := 0; i < 30; i++ {
		c.Add("а что насчёт соуса", strings.Repeat("томаты ", 50))
	}
	_ = c.Compact(context.Background())
	if !strings.Contains(gotText, "Прежний конспект:\nХронология: 14.11") {
		t.Fatal("прежний конспект не передан модели")
	}
	c.Reset()
	if c.Summary != "" || c.Context(chatKeepChars) != "" {
		t.Fatal("новый чат начинается без конспекта")
	}
}

func TestChatRecall(t *testing.T) {
	c := LoadChatThread(filepath.Join(t.TempDir(), "chat.json"))
	clock := time.Date(2026, 11, 14, 18, 5, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return clock })
	c.Add("посоветуй рецепт пиццы", "Возьми муку, дрожжи и томаты, выпекай при 250 градусах.")
	c.Add("спасибо", "Пожалуйста!")
	c.Add("какая завтра погода", "Завтра плюс три и дождь.")
	clock = clock.Add(24 * time.Hour)
	c.Add("включи свет", "[свет в гостиной включён]")
	clock = clock.Add(time.Hour)

	got := c.Search("что ты мне советовал про пиццу", "")
	if !strings.Contains(got, "вчера, суббота 14 ноября") || !strings.Contains(got, "18:05 Человек: посоветуй рецепт пиццы") ||
		!strings.Contains(got, "Джарвис: Возьми муку") || strings.Contains(got, "свет") {
		t.Fatalf("поиск по словам:\n%s", got)
	}
	got = c.Search("о чём говорили вчера", "")
	if !strings.Contains(got, "погода") || strings.Contains(got, "свет") {
		t.Fatalf("поиск за вчера:\n%s", got)
	}
	got = c.Search("весь день", "2026-11-15")
	if !strings.Contains(got, "свет в гостиной") || strings.Contains(got, "пицц") {
		t.Fatalf("поиск по дате:\n%s", got)
	}
	if got = c.Search("самолёт", ""); !strings.Contains(got, "ничего не нашлось") {
		t.Fatalf("не найдено:\n%s", got)
	}
	if got = c.Search("пицца", "2026-10-01"); !strings.Contains(got, "нет") {
		t.Fatalf("пустой день:\n%s", got)
	}
	tools := &Tools{Recall: c.Search}
	if r := tools.Call(context.Background(), "recall_conversation", `{"query":"пиццы"}`); !strings.Contains(r, "муку") {
		t.Fatalf("инструмент: %s", r)
	}
}

func TestMemoryWorthy(t *testing.T) {
	if memoryWorthy("Home Assistant недоступен — проверь адрес и включён ли сервер") {
		t.Fatal("ошибку запоминать не надо")
	}
	if memoryWorthy("Я ChatGPT, голосовой помощник для разговоров и идей") || memoryWorthy("Я большая языковая модель от Google") {
		t.Fatal("ответ, где модель назвала себя не Джарвисом, в память не берём")
	}
	if !memoryWorthy("Готово: «Mijia Robot Vacuum 5» vacuum.start-sweep") {
		t.Fatal("успешную команду надо запомнить")
	}
}

func TestMemoryChain(t *testing.T) {
	var s Settings
	if len(memoryChain(s)) != 0 {
		t.Fatal("без ключей конспект делать нечем")
	}
	s.GeminiKey, s.OpenAIKey = "g", "o"
	if c := memoryChain(s); len(c) != 2 || c[0].Provider != "gemini" || c[0].Model != "" || c[1].Provider != "openai" || c[1].Model != BackendModels[0] {
		t.Fatalf("авто: %+v", c)
	}
	s.Memory = MemorySettings{Provider: "openai", Model: BackendModels[1]}
	if c := memoryChain(s); len(c) != 1 || c[0] != (memoryStep{"openai", BackendModels[1]}) {
		t.Fatalf("выбран OpenAI: %+v", c)
	}
	s.Memory = MemorySettings{Provider: "chatgpt", Model: "gemini-3.8-flash"} // подписки нет — сам выбирает, модель не подходит — по умолчанию
	if c := memoryChain(s); len(c) != 2 || c[0] != (memoryStep{"gemini", "gemini-3.8-flash"}) || c[1].Model != BackendModels[0] {
		t.Fatalf("выбранного нет: %+v", c)
	}
	s.Memory = MemorySettings{Provider: "none"}
	if len(memoryChain(s)) != 0 {
		t.Fatal("«не делать конспект»")
	}
	s.Memory = MemorySettings{Off: true}
	if len(memoryChain(s)) != 0 {
		t.Fatal("память выключена")
	}
}

func TestMemoryOff(t *testing.T) {
	c := LoadChatThread(filepath.Join(t.TempDir(), "chat.json"))
	c.Add("привет", "здравствуй")
	off := true
	c.SetEnabled(func() bool { return !off })
	c.Add("секрет", "тсс")
	if c.Context(chatKeepChars) != "" || c.HasArchive() || !strings.Contains(c.Search("привет", ""), "выключена") {
		t.Fatal("выключенная память не должна попадать к модели")
	}
	off = false
	if len(c.archive) != 1 || !strings.Contains(c.Context(chatKeepChars), "привет") {
		t.Fatal("пока память выключена, реплики не пишутся; включили — прежнее на месте")
	}
}

// Досье копится отдельно от конспекта и переживает «Начать новый чат».
func TestChatProfile(t *testing.T) {
	c := LoadChatThread(filepath.Join(t.TempDir(), "chat.json"))
	for i := 0; i < 40; i++ {
		c.Add("меня зовут Сергей, я люблю острое", strings.Repeat("понял ", 50))
	}
	var got string
	c.SetSummarizer(func(ctx context.Context, instr, text string) (string, error) {
		got = text
		return "**О ХОЗЯИНЕ:**\n- Зовут Сергей, любит острое.\n\nДОГОВОРЁННОСТИ:\n- нет\n\nХРОНОЛОГИЯ:\n- 14.11 знакомились", nil
	})
	if err := c.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Profile != "- Зовут Сергей, любит острое." || !strings.HasPrefix(c.Summary, "ДОГОВОРЁННОСТИ:") || strings.Contains(c.Summary, "Сергей") {
		t.Fatalf("досье %q, конспект %q", c.Profile, c.Summary)
	}
	if !strings.Contains(got, "Досье на хозяина (прежнее):\nпока пусто") {
		t.Fatalf("модели: %.200s", got)
	}
	c.Reset()
	if ctx := c.Context(chatKeepChars); !strings.Contains(ctx, "Что ты знаешь о хозяине") || !strings.Contains(ctx, "Сергей") || strings.Contains(ctx, "знакомились") {
		t.Fatalf("после нового чата досье остаётся, конспект — нет:\n%s", ctx)
	}
}

// «Забудь, что меня зовут Сергей, меня зовут Павел»: архив чистится сразу, новое — в досье, остальное переписывает модель.
func TestChatForget(t *testing.T) {
	c := LoadChatThread(filepath.Join(t.TempDir(), "chat.json"))
	c.Profile = "- Зовут Сергей.\n- Любит пиццу."
	c.Summary = "ХРОНОЛОГИЯ:\n- 14.11 Сергей спрашивал рецепт"
	c.Add("меня зовут Сергей", "Приятно, Сергей!")
	c.Add("посоветуй пиццу", "Маргарита.")
	done := make(chan string, 1)
	c.SetSummarizer(func(ctx context.Context, instr, text string) (string, error) {
		done <- text
		return "О ХОЗЯИНЕ:\n- Зовут Павел (хозяин сам попросил запомнить, 16.11).\n- Любит пиццу.\nХРОНОЛОГИЯ:\n- 14.11 хозяин спрашивал рецепт", nil
	})
	r := c.EditMemory("что хозяина зовут Сергей", "хозяина зовут Павел", []string{"Сергей"})
	if !strings.Contains(r, "удалено реплик — 1") || !strings.Contains(c.Profile, "Павел (хозяин сам попросил запомнить") {
		t.Fatalf("ответ %q, досье %q", r, c.Profile)
	}
	if s := c.Search("Сергей", ""); strings.Contains(s, "Приятно") {
		t.Fatalf("реплика про Сергея должна уйти из архива:\n%s", s)
	}
	c.Add("забудь, что меня зовут Сергей", "Хорошо, Сергея забыл.") // сама просьба в память не идёт
	if len(c.archive) != 1 {
		t.Fatalf("в архиве %d реплик", len(c.archive))
	}
	select {
	case text := <-done:
		if !strings.Contains(text, "Забыть: что хозяина зовут Сергей") || !strings.Contains(text, "Запомнить: хозяина зовут Павел") {
			t.Fatalf("модели: %s", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("память не переписывается")
	}
	for i := 0; i < 50; i++ {
		c.mu.Lock()
		ok := !c.running && !strings.Contains(c.Profile, "Сергей")
		c.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ctx := c.Context(chatKeepChars); strings.Contains(ctx, "Сергей") || !strings.Contains(ctx, "Павел") {
		t.Fatalf("после правки:\n%s", ctx)
	}
}

// Архив переполнен — старые реплики уходят в летопись месяца, «вспомни» находит их там.
func TestChatChronicle(t *testing.T) {
	old := chatArchiveMax
	chatArchiveMax = 50
	defer func() { chatArchiveMax = old }()
	c := LoadChatThread(filepath.Join(t.TempDir(), "chat.json"))
	clock := time.Date(2026, 8, 3, 18, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return clock })
	for i := 0; i < 300; i++ {
		c.Add("как дела", "отлично")
		if i == 5 {
			c.Add("моя собака Жучка заболела", "Сочувствую, своди к ветеринару.")
		}
		clock = clock.Add(6 * time.Hour)
	}
	c.SetSummarizer(func(ctx context.Context, instr, text string) (string, error) {
		if strings.Contains(instr, "летопис") {
			return "03.08 хозяин рассказал, что его собака Жучка заболела; Джарвис посоветовал ветеринара.", nil
		}
		return "О ХОЗЯИНЕ:\n- есть собака\nХРОНОЛОГИЯ:\n- болтали", nil
	})
	c.mu.Lock()
	c.SummaryUpto = c.Next // всё уже в конспекте
	c.folding = true
	c.mu.Unlock()
	for i := 0; i < 20 && len(c.archive) > chatArchiveMax; i++ {
		if err := c.Fold(context.Background()); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		c.folding = true
		c.mu.Unlock()
	}
	if len(c.archive) > chatArchiveMax || len(c.Chronicle) == 0 || c.Chronicle[0].Month != "2026-08" {
		t.Fatalf("архив %d, летопись %+v", len(c.archive), c.Chronicle)
	}
	if s := c.Search("что с собакой", ""); !strings.Contains(s, "Из летописи") || !strings.Contains(s, "август 2026") || !strings.Contains(s, "Жучка") {
		t.Fatalf("поиск в летописи:\n%s", s)
	}
	if !strings.Contains(c.Context(chatKeepChars), "летопись: август 2026") {
		t.Fatal("модель должна знать, что есть летопись")
	}
	c2 := LoadChatThread(c.path)
	if len(c2.Chronicle) != len(c.Chronicle) || len(c2.archive) != len(c.archive) {
		t.Fatal("летопись и архив должны пережить перезапуск")
	}
}
