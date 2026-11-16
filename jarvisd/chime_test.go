package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDecodeMP3(t *testing.T) {
	f, err := os.Open("testdata/chime_test.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pcm, rate, err := decodeMP3(f)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 44100 {
		t.Errorf("частота %d, ждали 44100", rate)
	}
	// 0,25 с звука; MP3 добавляет задержку кодека, поэтому допускаем запас в один-два кадра (1152 отсчёта)
	if n := len(pcm); n < 11000 || n > 11025+3*1152 {
		t.Errorf("отсчётов %d, ждали около 11025", n)
	}
	var peak int16
	for _, s := range pcm {
		if s > peak {
			peak = s
		}
	}
	if peak < 3000 {
		t.Errorf("звук тихий или пустой: пик %d", peak)
	}
}

func TestDecodeMP3Garbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.mp3")
	os.WriteFile(p, []byte("это не mp3"), 0o644)
	f, _ := os.Open(p)
	defer f.Close()
	if pcm, _, err := decodeMP3(f); err == nil && len(pcm) > 0 {
		t.Error("мусор принят за звук")
	}
}

func TestResampleLinear(t *testing.T) {
	in := make([]int16, 441)
	for i := range in {
		in[i] = int16(i * 10)
	}
	out := resampleLinear(in, 44100, 48000)
	if n := len(out); n < 478 || n > 482 {
		t.Errorf("после 44100→48000 из 441 отсчёта получилось %d, ждали около 480", n)
	}
	if out[0] != 0 {
		t.Errorf("начало сместилось: %d", out[0])
	}
	if same := resampleLinear(in, 48000, 48000); len(same) != len(in) {
		t.Errorf("та же частота: %d вместо %d", len(same), len(in))
	}
}

// Сигнал активации: файл читается один раз, играет на выходной частоте и громкостью голоса.
func TestChimePlaysOnceDecoded(t *testing.T) {
	var mu sync.Mutex
	var rates []int
	var lens []int
	c := &Chime{Path: "testdata/chime_test.mp3", Volume: func() int { return 50 },
		run: func(_ context.Context, rate int, pcm []byte) error {
			mu.Lock()
			rates, lens = append(rates, rate), append(lens, len(pcm))
			mu.Unlock()
			return nil
		}}
	for i := 0; i < 3; i++ {
		if err := c.Play(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(rates) != 3 || rates[0] != OutRate || lens[0] < 2*11000 {
		t.Errorf("run: rates=%v lens=%v", rates, lens)
	}
	c.Path = "testdata/нет-такого.mp3" // после удачной загрузки файл больше не читается
	if err := c.Play(context.Background()); err != nil {
		t.Errorf("повторное воспроизведение не должно снова читать файл: %v", err)
	}
}

func TestChimeMissingFile(t *testing.T) {
	c := &Chime{Path: "testdata/нет-такого.mp3", run: func(context.Context, int, []byte) error { t.Error("играть нечего"); return nil }}
	if err := c.Play(context.Background()); err == nil {
		t.Error("ждали ошибку про отсутствующий файл")
	}
}

func TestEngineWakeChimeHonoursSwitch(t *testing.T) {
	st := &Store{s: defaultSettings(), path: filepath.Join(t.TempDir(), "settings.json")}
	plays := 0
	c := &Chime{Path: "testdata/chime_test.mp3", run: func(context.Context, int, []byte) error { plays++; return nil }}
	e := &Engine{store: st, chime: c}

	e.playWakeChime(context.Background())
	if plays != 1 {
		t.Fatalf("по умолчанию сигнал включён, играл %d раз", plays)
	}
	_ = st.Update(func(s *Settings) error { s.WakeChimeOff = true; return nil })
	e.playWakeChime(context.Background())
	if plays != 1 {
		t.Fatalf("сигнал выключен, а играл %d раз", plays)
	}
	(&Engine{store: st}).playWakeChime(context.Background()) // без Chime — не падает
}
