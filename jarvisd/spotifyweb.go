package main

// Панель: карточка «Spotify» в подключениях — найден ли Soloist, ключи, перезапуск.

import (
	"context"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// yamRootOf — корень YAM, если Soloist поставлен через него (…/share/horohi.soloist).
func yamRootOf(root string) (string, bool) {
	if path.Base(root) == "horohi.soloist" && path.Base(path.Dir(root)) == "share" {
		return path.Dir(path.Dir(root)), true
	}
	return "", false
}

// soloistConfDir — где лежат config.env и spotify_api.conf: у YAM в etc, у автономной установки рядом с Soloist.
func soloistConfDir(root string) string {
	if yam, ok := yamRootOf(root); ok {
		return path.Join(yam, "etc", "horohi.soloist")
	}
	return root
}

// parseSoloistEnv — строки KEY=value / KEY="value"; комментарии и пустые пропускаются.
func parseSoloistEnv(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if line == "" || strings.HasPrefix(line, "#") || !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}

// validSoloistValue — файлы читает sh, поэтому в значении нет ни кавычек, ни подстановок, ни переводов строк.
func validSoloistValue(v string) bool {
	if len(v) > 200 {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("\"\\$`", r) {
			return false
		}
	}
	return true
}

type spotifyConfigUpdate struct{ APIKey, DeviceName, ClientID, ClientSecret *string }

type envChange struct {
	key string
	val *string
}

// setEnvLine — заменить строку KEY=… или дописать в конец; остальные строки и комментарии не трогаем.
func setEnvLine(content, key, val string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	repl := key + `="` + val + `"`
	for i, l := range lines {
		if k, _, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == key {
			lines[i] = repl
			return strings.Join(lines, "\n") + "\n"
		}
	}
	return strings.Join(append(lines, repl), "\n") + "\n"
}

func updateEnvFile(file string, changes []envChange) error {
	changed := false
	for _, c := range changes {
		changed = changed || c.val != nil
	}
	if !changed {
		return nil
	}
	old, _ := os.ReadFile(file)
	content := string(old)
	for _, c := range changes {
		if c.val != nil {
			content = setEnvLine(content, c.key, *c.val)
		}
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func writeSoloistConfig(dir string, u spotifyConfigUpdate) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := updateEnvFile(path.Join(dir, "config.env"), []envChange{{"API_KEY", u.APIKey}, {"DEVICE_NAME", u.DeviceName}}); err != nil {
		return err
	}
	return updateEnvFile(path.Join(dir, "spotify_api.conf"), []envChange{{"SPOTIFY_CLIENT_ID", u.ClientID}, {"SPOTIFY_CLIENT_SECRET", u.ClientSecret}})
}

func (w *Web) spotifySoloist() *Spotify {
	if w.engine == nil {
		return nil
	}
	return w.engine.spotify
}

func soloistSettings(sp *Spotify) (env, api map[string]string) {
	dir := soloistConfDir(sp.Root())
	b1, _ := os.ReadFile(path.Join(dir, "config.env"))
	b2, _ := os.ReadFile(path.Join(dir, "spotify_api.conf"))
	return parseSoloistEnv(b1), parseSoloistEnv(b2)
}

func (w *Web) spotifyState() map[string]any {
	sp := w.spotifySoloist()
	if !sp.Installed() {
		return map[string]any{"installed": false}
	}
	env, api := soloistSettings(sp)
	_, yam := yamRootOf(sp.Root())
	return map[string]any{"installed": true, "running": sp.Running(), "playing": sp.Playing(), "root": sp.Root(), "yam": yam,
		"device_name": env["DEVICE_NAME"], "api_key": mask(env["API_KEY"]),
		"client_id": mask(api["SPOTIFY_CLIENT_ID"]), "client_secret": mask(api["SPOTIFY_CLIENT_SECRET"])}
}

// putSpotify — записать ключи в конфиги Soloist. Не пришедшее поле не трогаем; маска вместо ключа значит «без изменений».
func (w *Web) putSpotify(rw http.ResponseWriter, r *http.Request) {
	sp := w.spotifySoloist()
	if !sp.Installed() {
		bad(rw, "Soloist не найден на колонке")
		return
	}
	var req map[string]*string
	if readJSON(r, &req) != nil {
		bad(rw, "пустой запрос")
		return
	}
	env, api := soloistSettings(sp)
	var u spotifyConfigUpdate
	for _, f := range []struct {
		name   string
		secret bool
		cur    string
		dst    **string
	}{
		{"api_key", true, env["API_KEY"], &u.APIKey},
		{"device_name", false, env["DEVICE_NAME"], &u.DeviceName},
		{"client_id", true, api["SPOTIFY_CLIENT_ID"], &u.ClientID},
		{"client_secret", true, api["SPOTIFY_CLIENT_SECRET"], &u.ClientSecret},
	} {
		v := req[f.name]
		if v == nil {
			continue
		}
		val := strings.TrimSpace(*v)
		if (f.secret && val == mask(f.cur)) || val == f.cur {
			continue
		}
		if !validSoloistValue(val) {
			bad(rw, "недопустимые символы в поле "+f.name)
			return
		}
		*f.dst = &val
	}
	if err := writeSoloistConfig(soloistConfDir(sp.Root()), u); err != nil {
		log.Printf("spotify: запись настроек Soloist: %v", err)
		writeJSON(rw, 500, map[string]any{"error": "не удалось записать настройки Soloist"})
		return
	}
	writeJSON(rw, 200, w.spotifyState())
}

func (w *Web) spotifyRestart(rw http.ResponseWriter, r *http.Request) {
	sp := w.spotifySoloist()
	if !sp.Installed() {
		bad(rw, "Soloist не найден на колонке")
		return
	}
	yam, ok := yamRootOf(sp.Root())
	if !ok {
		bad(rw, "Soloist установлен без YAM: перезапустите Soloist вручную")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sp.exec(ctx, path.Join(yam, "bin", "soloist"), "restart"); err != nil {
		log.Printf("spotify: перезапуск Soloist: %v", err)
		bad(rw, "не удалось перезапустить Soloist")
		return
	}
	writeJSON(rw, 200, w.spotifyState())
}
