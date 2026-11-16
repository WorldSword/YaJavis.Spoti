package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreviewLastWins(t *testing.T) {
	w := &Web{}
	ctx1, seq1, rel1 := w.previewTake()
	got := make(chan uint64)
	go func() { // вторая проба ждёт, пока первая отпустит
		_, seq2, rel2 := w.previewTake()
		got <- seq2
		rel2()
	}()
	select {
	case <-ctx1.Done():
	case <-time.After(time.Second):
		t.Fatal("первую пробу не отменили")
	}
	if w.previewLatest(seq1) {
		t.Fatal("первая проба считается последней")
	}
	select {
	case <-got:
		t.Fatal("вторая проба не дождалась первой")
	case <-time.After(50 * time.Millisecond):
	}
	rel1()
	if s := <-got; !w.previewLatest(s) {
		t.Fatal("вторая проба не последняя")
	}
}

func TestVoiceFileModelAndPrune(t *testing.T) {
	w := &Web{dataDir: t.TempDir()}
	a, b := w.voiceFile("chatgpt-ash", "", "gpt-live-1"), w.voiceFile("chatgpt-ash", "", "gpt-live-1.1")
	if a == b {
		t.Fatal("модель не влияет на ключ кеша")
	}
	other := w.voiceFile("chatgpt-ashley", "", "gpt-live-1")
	_ = os.MkdirAll(filepath.Dir(a), 0o700)
	_ = os.WriteFile(a, make([]byte, 100), 0o600)
	_ = os.WriteFile(other, make([]byte, 100), 0o600)
	saveVoice(b, make([]int16, MicRate))
	if _, err := os.Stat(a); err == nil {
		t.Fatal("образец прежней модели не удалён")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("удалён образец другого голоса")
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatal("новый образец не сохранён")
	}
}
