package main

// jarvisd patch-launch <launch.json> <out.json> <vqe_tap.so> <mic.sock>
// Копия штатного профиля запуска Quasar, где процессу maind --platformd добавлены
// LD_PRELOAD=vqe_tap.so и JARVIS_MIC_SOCKET. Системный файл не меняется: init-скрипт
// подкладывает копию bind-mount'ом, удаление/откат — просто не монтировать.
// Делается на каждой загрузке — после обновления прошивки профиль берётся свежий.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func patchLaunch(in, out, lib, sock string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("launch.json не JSON: %w", err)
	}
	patched := 0
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if path, _ := x["path"].(string); strings.HasSuffix(path, "/maind") && hasArg(x["arg"], "--platformd") {
				x["envs"] = withEnv(x["envs"], lib, sock)
				patched++
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(doc)
	if patched == 0 {
		return errors.New("в launch.json нет процесса maind --platformd — микрофон на этой прошивке не подключить")
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

func hasArg(v any, want string) bool {
	arr, _ := v.([]any)
	for _, a := range arr {
		if s, _ := a.(string); s == want {
			return true
		}
	}
	return false
}

func withEnv(v any, lib, sock string) []any {
	arr, _ := v.([]any)
	out := make([]any, 0, len(arr)+2)
	preload := lib
	for _, e := range arr {
		s, _ := e.(string)
		switch {
		case strings.HasPrefix(s, "LD_PRELOAD="):
			for _, p := range strings.Split(strings.TrimPrefix(s, "LD_PRELOAD="), ":") {
				if p != "" && !strings.HasSuffix(p, "/vqe_tap.so") { // чужой tap заменяем своим
					preload += ":" + p
				}
			}
		case strings.HasPrefix(s, "JARVIS_MIC_SOCKET="), strings.HasPrefix(s, "ALICE_CAPTURE_SOCKET="), strings.HasPrefix(s, "JARVIS_MIC_CHANNEL="):
		default:
			out = append(out, e)
		}
	}
	out = append(out, "LD_PRELOAD="+preload, "JARVIS_MIC_SOCKET="+sock)
	// mic-channel рядом с vqe_tap.so («6:0») — другой канал VQE, если на прошивке он не тип 6 (см. jarvisd diag-mic)
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(lib), "mic-channel")); err == nil {
		if ch := strings.TrimSpace(string(b)); micChannelRe.MatchString(ch) {
			out = append(out, "JARVIS_MIC_CHANNEL="+ch)
		}
	}
	return out
}

var micChannelRe = regexp.MustCompile(`^\d{1,2}:\d$`)
