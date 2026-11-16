package soloist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tuner подбирает для колонки рабочее ALSA-устройство вывода Soloist.
// На разных моделях рабочее устройство разное (на Станции 2 plug:default не открывается), поэтому по очереди
// запускаем Soloist с каждым вариантом, включаем короткий трек и смотрим, что звуковая карта реально играет.
// Скрипты запуска меняются один раз, только когда рабочий вариант найден.

const (
	probeTrack = "spotify:track:3DwQ7AH3xGD9h65ezslm6q"
	probeLog   = "/tmp/soloist_tune.log"
	ctlAddr    = "127.0.0.1:9090"
)

var cardRe = regexp.MustCompile(`(?m)^\s*\d+\s+\[([^\]\s]+)\s*\]`)

// TuneState — состояние подбора для панели. State: idle, running, done, error.
type TuneState struct {
	State  string `json:"state"`
	Step   string `json:"step,omitempty"`
	Device string `json:"device,omitempty"`
	Err    string `json:"error,omitempty"`
}

type Tuner struct {
	Root    string
	Run     Exec
	Sleep   func(time.Duration)
	Restart func(ctx context.Context) error // обычный перезапуск Soloist после подбора

	mu sync.Mutex
	st TuneState
}

func NewTuner(root string, run Exec, restart func(context.Context) error) *Tuner {
	return &Tuner{Root: root, Run: run, Sleep: time.Sleep, Restart: restart, st: TuneState{State: "idle"}}
}

func (t *Tuner) Status() TuneState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

func (t *Tuner) set(f func(*TuneState)) {
	t.mu.Lock()
	f(&t.st)
	t.mu.Unlock()
}

// Start запускает подбор в фоне. Одновременно идёт только один.
func (t *Tuner) Start() error {
	t.mu.Lock()
	if t.st.State == "running" {
		t.mu.Unlock()
		return errors.New("подбор звука уже идёт")
	}
	t.st = TuneState{State: "running", Step: "начинаю"}
	t.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		t.run(ctx)
	}()
	return nil
}

// Tune подбирает устройство синхронно и возвращает выбранное.
func (t *Tuner) Tune(ctx context.Context) (string, error) {
	t.mu.Lock()
	t.st = TuneState{State: "running", Step: "начинаю"}
	t.mu.Unlock()
	return t.run(ctx)
}

func (t *Tuner) run(ctx context.Context) (string, error) {
	dev, err := t.tune(ctx)
	t.set(func(s *TuneState) {
		if err != nil {
			*s = TuneState{State: "error", Err: err.Error()}
		} else {
			*s = TuneState{State: "done", Device: dev}
		}
	})
	return dev, err
}

func (t *Tuner) sh(ctx context.Context, script string) ([]byte, error) {
	return t.Run(ctx, "sh", "-c", script, "sh", t.Root)
}

// candidates — варианты устройства в порядке проб: штатный, по карте, остальные.
func candidates(cards string) []string {
	out := []string{"plug:default"}
	seen := map[string]bool{}
	for _, m := range cardRe.FindAllStringSubmatch(cards, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, "sysdefault:CARD="+m[1])
		}
	}
	return append(out, "default", "dmix_spk_plug")
}

func (t *Tuner) stopSoloist(ctx context.Context) {
	t.sh(ctx, `sh "$1/stop.sh" >/dev/null 2>&1; exit 0`)
}

func (t *Tuner) tune(ctx context.Context) (string, error) {
	for _, f := range []string{"start.sh", "stop.sh"} {
		if st, err := os.Stat(filepath.Join(t.Root, f)); err != nil || st.IsDir() {
			return "", fmt.Errorf("подбор звука работает с автономной установкой Soloist: нет %s в %s", f, t.Root)
		}
	}
	cards, _ := t.sh(ctx, `cat /proc/asound/cards 2>/dev/null; exit 0`)
	var tried []string
	win := ""
	for _, dev := range candidates(string(cards)) {
		if ctx.Err() != nil {
			break
		}
		t.set(func(s *TuneState) { s.Step = "проверяю " + dev })
		ok, err := t.probe(ctx, dev)
		if err != nil {
			t.stopSoloist(ctx)
			t.Restart(ctx)
			return "", err
		}
		tried = append(tried, dev)
		if ok {
			win = dev
			break
		}
	}
	t.stopSoloist(ctx)
	if win == "" {
		t.Restart(ctx)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("звук не заработал ни на одном варианте (%s). Проверьте, что Soloist привязан к Spotify и колонка не выключена", strings.Join(tried, ", "))
	}
	t.set(func(s *TuneState) { s.Step = "записываю " + win })
	if err := SetAudioDevice(t.Root, win); err != nil {
		t.Restart(ctx)
		return "", err
	}
	t.set(func(s *TuneState) { s.Step = "перезапускаю Soloist" })
	if err := t.Restart(ctx); err != nil {
		return win, fmt.Errorf("устройство %s записано, но Soloist не перезапустился: %w", win, err)
	}
	return win, nil
}

// probe запускает Soloist с устройством dev, включает короткий трек и проверяет вывод звука.
func (t *Tuner) probe(ctx context.Context, dev string) (bool, error) {
	if !deviceRe.MatchString(dev) {
		return false, fmt.Errorf("недопустимое имя устройства: %q", dev)
	}
	t.stopSoloist(ctx)
	t.Sleep(2 * time.Second)
	start := `# TUNE_START
S="$1"; . "$S/config.env"
export LD_LIBRARY_PATH="$S/lib64" ALSA_PLUGIN_DIR="$S/lib64/alsa-lib" APULSE_EXTERNAL_VOLUME=1 APULSE_DIAG=1
APULSE_PLAYBACK_DEVICE=` + dev + ` nohup "$S/bin/soloist" -v -n "$DEVICE_NAME" -k "$API_KEY" -D "$S/data" -C /tmp/soloist_cache -w ` + ctlAddr + ` -i 50 >` + probeLog + ` 2>&1 </dev/null &
exit 0`
	if _, err := t.sh(ctx, start); err != nil {
		return false, fmt.Errorf("не удалось запустить Soloist: %w", err)
	}
	t.Sleep(8 * time.Second)
	ctl := `# TUNE_CTL
S="$1"; export LD_LIBRARY_PATH="$S/lib64"
"$S/bin/soloist" ctl -w ` + ctlAddr + ` activate >/dev/null 2>&1
sleep 2
"$S/bin/soloist" ctl -w ` + ctlAddr + ` play ` + probeTrack + ` >/dev/null 2>&1
exit 0`
	t.sh(ctx, ctl)
	t.Sleep(7 * time.Second)
	check := `# TUNE_CHECK
state=closed
for f in /proc/asound/card*/pcm*p/sub*/status; do [ -r "$f" ] && grep -q RUNNING "$f" && state=RUNNING; done
echo "state=$state errors=$(grep -c 'pcm open.*failed' ` + probeLog + ` 2>/dev/null)"
exit 0`
	out, err := t.sh(ctx, check)
	if err != nil {
		return false, nil
	}
	s := string(out)
	n := -1
	if i := strings.Index(s, "errors="); i >= 0 {
		n, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(s[i+7:], "\n", 2)[0]))
	}
	return strings.Contains(s, "state=RUNNING") && n == 0, nil
}
