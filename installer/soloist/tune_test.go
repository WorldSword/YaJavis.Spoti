package soloist

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSpeaker притворяется колонкой: знает, какие устройства «работают», и отвечает на команды Tuner.
type fakeSpeaker struct {
	mu      sync.Mutex
	cards   string
	works   map[string]bool
	current string
	calls   []string
}

func (f *fakeSpeaker) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	switch {
	case strings.Contains(line, "/proc/asound/cards"):
		return []byte(f.cards), nil
	case strings.Contains(line, "TUNE_START"):
		for dev := range f.works {
			if strings.Contains(line, "APULSE_PLAYBACK_DEVICE="+dev+" ") {
				f.current = dev
			}
		}
		return nil, nil
	case strings.Contains(line, "TUNE_CHECK"):
		if f.works[f.current] {
			return []byte("state=RUNNING errors=0\n"), nil
		}
		return []byte("state=closed errors=3\n"), nil
	}
	return nil, nil
}

func (f *fakeSpeaker) started() []string {
	var out []string
	for _, c := range f.calls {
		if strings.Contains(c, "TUNE_START") {
			for _, w := range strings.Fields(c) {
				if strings.HasPrefix(w, "APULSE_PLAYBACK_DEVICE=") {
					out = append(out, strings.TrimPrefix(w, "APULSE_PLAYBACK_DEVICE="))
				}
			}
		}
	}
	return out
}

func tunerRoot(t *testing.T) string {
	t.Helper()
	dir, _ := writeService(t, serviceFixture)
	for _, f := range []string{"start.sh", "stop.sh"} {
		body := "#!/bin/sh\n"
		if f == "start.sh" {
			body += "export APULSE_PLAYBACK_DEVICE=\"plug:default\"\n"
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newTestTuner(root string, f *fakeSpeaker, restarts *int) *Tuner {
	tn := NewTuner(root, f.run, func(context.Context) error { *restarts++; return nil })
	tn.Sleep = func(time.Duration) {}
	return tn
}

const cardsAML = " 0 [AMLAUGESOUND  ]: AML-AUGESOUND - AML-AUGESOUND\n                      AML-AUGESOUND\n"

func scriptDevice(t *testing.T, root string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(root, "start.sh"))
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "export APULSE_PLAYBACK_DEVICE=") {
			return strings.Trim(strings.TrimPrefix(strings.TrimSpace(l), "export APULSE_PLAYBACK_DEVICE="), `"`)
		}
	}
	return ""
}

// Станция 2: штатный plug:default не открывается, а sysdefault:CARD=… работает.
func TestTuneFindsWorkingDevice(t *testing.T) {
	root := tunerRoot(t)
	f := &fakeSpeaker{cards: cardsAML, works: map[string]bool{"plug:default": false, "sysdefault:CARD=AMLAUGESOUND": true, "default": true}}
	var restarts int
	tn := newTestTuner(root, f, &restarts)

	dev, err := tn.Tune(context.Background())
	if err != nil || dev != "sysdefault:CARD=AMLAUGESOUND" {
		t.Fatalf("dev=%q err=%v", dev, err)
	}
	if got := f.started(); len(got) != 2 || got[0] != "plug:default" || got[1] != "sysdefault:CARD=AMLAUGESOUND" {
		t.Errorf("порядок проб: %v", got)
	}
	if d := scriptDevice(t, root); d != "sysdefault:CARD=AMLAUGESOUND" {
		t.Errorf("в start.sh записано %q", d)
	}
	if restarts != 1 {
		t.Errorf("перезапусков Soloist: %d, ждали 1", restarts)
	}
	if s := tn.Status(); s.State != "done" || s.Device != "sysdefault:CARD=AMLAUGESOUND" {
		t.Errorf("статус: %+v", s)
	}
}

// Колонка, где штатный вариант работает: сразу его и оставляем, дальше не пробуем.
func TestTuneKeepsStockDeviceWhenItWorks(t *testing.T) {
	root := tunerRoot(t)
	f := &fakeSpeaker{cards: cardsAML, works: map[string]bool{"plug:default": true, "sysdefault:CARD=AMLAUGESOUND": true}}
	var restarts int
	dev, err := newTestTuner(root, f, &restarts).Tune(context.Background())
	if err != nil || dev != "plug:default" {
		t.Fatalf("dev=%q err=%v", dev, err)
	}
	if got := f.started(); len(got) != 1 {
		t.Errorf("пробовали лишнее: %v", got)
	}
}

// Ничего не заработало: скрипты остаются как были, Soloist запускается обратно.
func TestTuneFailureLeavesScriptsAlone(t *testing.T) {
	root := tunerRoot(t)
	before, _ := os.ReadFile(filepath.Join(root, "start.sh"))
	f := &fakeSpeaker{cards: cardsAML, works: map[string]bool{"plug:default": false, "sysdefault:CARD=AMLAUGESOUND": false, "default": false}}
	var restarts int
	tn := newTestTuner(root, f, &restarts)
	_, err := tn.Tune(context.Background())
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	after, _ := os.ReadFile(filepath.Join(root, "start.sh"))
	if string(before) != string(after) {
		t.Errorf("start.sh изменён:\n%s", after)
	}
	if restarts != 1 {
		t.Errorf("Soloist надо вернуть в работу: перезапусков %d", restarts)
	}
	if s := tn.Status(); s.State != "error" || s.Err == "" {
		t.Errorf("статус: %+v", s)
	}
}

// Одна и та же карта не пробуется дважды; несколько карт — каждая со своим sysdefault.
func TestTuneCandidates(t *testing.T) {
	cards := " 0 [AMLAUGESOUND  ]: AML\n 1 [USBDAC         ]: USB\n"
	got := candidates(cards)
	want := []string{"plug:default", "sysdefault:CARD=AMLAUGESOUND", "sysdefault:CARD=USBDAC", "default", "dmix_spk_plug"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("candidates = %v", got)
	}
	if got := candidates(""); strings.Join(got, ",") != "plug:default,default,dmix_spk_plug" {
		t.Errorf("без карт: %v", got)
	}
}

func TestTuneNeedsStandaloneScripts(t *testing.T) {
	var restarts int
	tn := newTestTuner(t.TempDir(), &fakeSpeaker{}, &restarts)
	if _, err := tn.Tune(context.Background()); err == nil {
		t.Error("без start.sh ждали ошибку")
	}
	if restarts != 0 {
		t.Errorf("пустую папку не трогаем: перезапусков %d", restarts)
	}
}

func TestTunerStartIsSingleFlight(t *testing.T) {
	root := tunerRoot(t)
	f := &fakeSpeaker{cards: cardsAML, works: map[string]bool{"plug:default": true}}
	release := make(chan struct{})
	var restarts int
	tn := NewTuner(root, f.run, func(context.Context) error { restarts++; return nil })
	tn.Sleep = func(time.Duration) { <-release }
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	if err := tn.Start(); err == nil {
		t.Error("второй запуск при работающем должен вернуть ошибку")
	}
	if s := tn.Status(); s.State != "running" {
		t.Errorf("статус: %+v", s)
	}
	close(release)
	for i := 0; i < 200 && tn.Status().State == "running"; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if s := tn.Status(); s.State != "done" {
		t.Errorf("после работы: %+v", s)
	}
}

func TestTuneRestartError(t *testing.T) {
	root := tunerRoot(t)
	f := &fakeSpeaker{cards: cardsAML, works: map[string]bool{"plug:default": true}}
	tn := NewTuner(root, f.run, func(context.Context) error { return errors.New("не запустился") })
	tn.Sleep = func(time.Duration) {}
	if _, err := tn.Tune(context.Background()); err == nil {
		t.Error("ошибку перезапуска нельзя прятать")
	}
}

// Настоящая оболочка должна разбирать все скрипты Tuner (на колонке busybox sh, тут проверяем синтаксис).
func TestTuneScriptsParse(t *testing.T) {
	root := tunerRoot(t)
	var bad []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "sh" && len(args) > 1 && args[0] == "-c" {
			out, err := exec.Command("sh", append([]string{"-n", "-c"}, args[1:]...)...).CombinedOutput()
			if err != nil {
				bad = append(bad, string(out))
			}
			return []byte("state=closed errors=1\n"), nil
		}
		return nil, nil
	}
	tn := NewTuner(root, run, func(context.Context) error { return nil })
	tn.Sleep = func(time.Duration) {}
	tn.Tune(context.Background())
	if len(bad) > 0 {
		t.Errorf("скрипты не разбираются: %v", bad)
	}
}
