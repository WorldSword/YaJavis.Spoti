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

// Установщик не меняет поведение апстрима, а только обводит строку устройства метками для Tuner.
func TestPatchAudioDeviceAddsMarks(t *testing.T) {
	dir, file := writeService(t, serviceFixture)
	changed, err := patchAudioDevice(dir)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(file)
	s := string(b)
	want := "        # jarvis-audio begin\n        export APULSE_PLAYBACK_DEVICE=plug:default\n        # jarvis-audio end\n"
	if !strings.Contains(s, want) {
		t.Errorf("нет блока с метками:\n%s", s)
	}
	if !strings.Contains(s, `export APULSE_PLAYBACK_DEVICE="sysdefault:CARD=$CARD_NAME"`) {
		t.Errorf("ветка plum потеряна:\n%s", s)
	}
	if out, err := exec.Command("sh", "-n", file).CombinedOutput(); err != nil {
		t.Errorf("скрипт больше не разбирается оболочкой: %v %s", err, out)
	}
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

// start.sh апстрима задаёт устройство в кавычках.
func TestSetAudioDeviceStartScript(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "start.sh")
	body := "case \"$COMPAT\" in\n    *plum*)\n        export APULSE_PLAYBACK_DEVICE=\"sysdefault:CARD=$CARD_NAME\"\n        ;;\n    *)\n        export APULSE_PLAYBACK_DEVICE=\"plug:default\"\n        ;;\nesac\n"
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SetAudioDevice(dir, "sysdefault:CARD=AMLAUGESOUND"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "        export APULSE_PLAYBACK_DEVICE=sysdefault:CARD=AMLAUGESOUND\n        # jarvis-audio end") {
		t.Errorf("start.sh не исправлен:\n%s", b)
	}
	if strings.Contains(string(b), `"plug:default"`) {
		t.Errorf("осталась прежняя строка:\n%s", b)
	}
	if out, err := exec.Command("sh", "-n", file).CombinedOutput(); err != nil {
		t.Errorf("start.sh не разбирается оболочкой: %v %s", err, out)
	}
	// второй выбор меняет значение между метками, а не плодит блоки
	if err := SetAudioDevice(dir, "plug:default"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	if strings.Count(string(b), markBegin) != 1 || !strings.Contains(string(b), "export APULSE_PLAYBACK_DEVICE=plug:default\n        # jarvis-audio end") {
		t.Errorf("повторный выбор:\n%s", b)
	}
}

// Установки прошлых версий: блок с именем карты и правка fix-sound.sh превращаются в блок с метками.
func TestSetAudioDeviceLegacyForms(t *testing.T) {
	legacy := strings.Replace(serviceFixture, "export APULSE_PLAYBACK_DEVICE=plug:default", legacyCardBlock, 1)
	fixed := strings.Replace(serviceFixture, "export APULSE_PLAYBACK_DEVICE=plug:default", "export APULSE_PLAYBACK_DEVICE=sysdefault:CARD=AMLAUGESOUND", 1)
	for name, body := range map[string]string{"legacy": legacy, "fix-sound": fixed} {
		dir, file := writeService(t, body)
		if err := SetAudioDevice(dir, "default"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(file)
		if !strings.Contains(string(b), "export APULSE_PLAYBACK_DEVICE=default\n        # jarvis-audio end") || strings.Contains(string(b), "$(awk") {
			t.Errorf("%s:\n%s", name, b)
		}
		if out, err := exec.Command("sh", "-n", file).CombinedOutput(); err != nil {
			t.Errorf("%s не разбирается оболочкой: %v %s", name, err, out)
		}
	}
}

func TestSetAudioDeviceRejectsBadInput(t *testing.T) {
	dir, _ := writeService(t, serviceFixture)
	for _, dev := range []string{"", "a b", "x;reboot", "$(id)", "a\nb"} {
		if err := SetAudioDevice(dir, dev); err == nil {
			t.Errorf("устройство %q принято", dev)
		}
	}
	if err := SetAudioDevice(t.TempDir(), "default"); err == nil {
		t.Error("без скриптов ждали ошибку")
	}
	other, _ := writeService(t, "#!/bin/sh\nexport SOMETHING=1\n")
	if err := SetAudioDevice(other, "default"); err == nil {
		t.Error("незнакомый скрипт: ждали ошибку")
	}
}
