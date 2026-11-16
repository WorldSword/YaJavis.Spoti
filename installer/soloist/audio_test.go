package soloist

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const serviceFixture = `case "$COMPAT" in
    *plum*)
        export APULSE_PLAYBACK_DEVICE="sysdefault:CARD=$CARD_NAME"
        ;;
    *)
        export APULSE_PLAYBACK_DEVICE=plug:default
        ;;
esac
export APULSE_DIAG=0
`

func writeService(t *testing.T, body string) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "scripts", "soloist-service.sh")
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

// Станция 2: через plug:default Soloist не открывает устройство (No such device or address),
// а sysdefault:CARD=<карта> работает. Перед установкой правим служебный скрипт.
func TestPatchAudioDevice(t *testing.T) {
	dir, file := writeService(t, serviceFixture)
	changed, err := patchAudioDevice(dir)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(file)
	s := string(b)
	if strings.Count(s, "plug:default") != 1 || !strings.Contains(s, `sysdefault:CARD=$CARD_NAME`) {
		t.Errorf("скрипт после правки:\n%s", s)
	}
	if !strings.Contains(s, `export APULSE_PLAYBACK_DEVICE="sysdefault:CARD=$CARD_NAME"
        ;;`) {
		t.Errorf("ветка plum потеряна:\n%s", s)
	}
	if out, err := exec.Command("sh", "-n", file).CombinedOutput(); err != nil {
		t.Errorf("скрипт больше не разбирается оболочкой: %v %s", err, out)
	}
	// повторный запуск ничего не меняет
	if again, err := patchAudioDevice(dir); err != nil || again {
		t.Errorf("повторная правка: changed=%v err=%v", again, err)
	}
}

func TestPatchAudioDeviceLeavesUnknownScripts(t *testing.T) {
	dir, file := writeService(t, "#!/bin/sh\nexport SOMETHING=1\n")
	changed, err := patchAudioDevice(dir)
	if err != nil || changed {
		t.Errorf("незнакомый скрипт: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(file); string(b) != "#!/bin/sh\nexport SOMETHING=1\n" {
		t.Errorf("скрипт изменился: %q", b)
	}
	if changed, err := patchAudioDevice(t.TempDir()); err != nil || changed {
		t.Errorf("нет скрипта: changed=%v err=%v", changed, err)
	}
}

// Имя карты берётся из /proc/asound/cards, пробелы вокруг имени отбрасываются.
func TestCardNameSnippet(t *testing.T) {
	cards := " 0 [AMLAUGESOUND  ]: AML-AUGESOUND - AML-AUGESOUND\n                      AML-AUGESOUND\n"
	f := filepath.Join(t.TempDir(), "cards")
	if err := os.WriteFile(f, []byte(cards), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := strings.Replace(cardNameShell, "/proc/asound/cards", f, 1) + `; printf %s "$CARD_NAME"`
	out, err := exec.Command("sh", "-c", sh).Output()
	if err != nil || string(out) != "AMLAUGESOUND" {
		t.Errorf("имя карты %q, ошибка %v", out, err)
	}
}
