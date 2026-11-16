package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileHasAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lib.so")
	body := bytes.Repeat([]byte{0}, 64<<10-5) // символ ложится на границу кусков
	body = append(body, []byte(vqeSymbol)...)
	body = append(body, bytes.Repeat([]byte{1}, 100)...)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileHas(p, vqeSymbol) {
		t.Fatal("не нашёл символ на границе кусков")
	}
	if fileHas(p, "YandexVqeC_Missing") {
		t.Fatal("нашёл то, чего нет")
	}
}

func TestMappedLibs(t *testing.T) {
	maps := "b6f00000-b6f01000 r-xp 00000000 1f:00 12 /system/vendor/lib/libvqe.so\n" +
		"b6f01000-b6f02000 r--p 00001000 1f:00 12 /system/vendor/lib/libvqe.so\n" +
		"b6f02000-b6f03000 rw-p 00000000 00:00 0 [heap]\n"
	if got := mappedLibs(maps); len(got) != 1 || got[0] != "/system/vendor/lib/libvqe.so" {
		t.Fatalf("mappedLibs = %v", got)
	}
}

func TestPatchLaunchMicChannel(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "launch.json"), filepath.Join(dir, "out.json")
	_ = os.WriteFile(in, []byte(`{"p":[{"path":"/system/vendor/quasar/maind","arg":["--platformd"],"envs":["A=1"]}]}`), 0o644)
	lib := filepath.Join(dir, "vqe_tap.so")
	if err := patchLaunch(in, out, lib, "/tmp/m.sock"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); strings.Contains(string(b), "JARVIS_MIC_CHANNEL") {
		t.Fatal("канал без файла mic-channel")
	}
	_ = os.WriteFile(filepath.Join(dir, "mic-channel"), []byte("7:1\n"), 0o644)
	if err := patchLaunch(in, out, lib, "/tmp/m.sock"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "JARVIS_MIC_CHANNEL=7:1") {
		t.Fatalf("нет канала: %s", b)
	}
	_ = os.WriteFile(filepath.Join(dir, "mic-channel"), []byte("7:1; rm -rf /"), 0o644)
	_ = patchLaunch(in, out, lib, "/tmp/m.sock")
	if b, _ := os.ReadFile(out); strings.Contains(string(b), "JARVIS_MIC_CHANNEL") {
		t.Fatal("мусор из mic-channel попал в профиль")
	}
}

func TestGeminiGoneErr(t *testing.T) {
	if !geminiGoneErr("Gemini ответил 404: This model models/gemini-2.5-flash-lite is no longer available to new users") {
		t.Fatal("404 не распознан")
	}
	if geminiGoneErr("Gemini ответил 400: API key not valid") || geminiGoneErr("quota: Gemini ответил 429") {
		t.Fatal("ложное срабатывание")
	}
}
