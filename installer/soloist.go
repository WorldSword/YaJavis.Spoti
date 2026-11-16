package main

// Установка Soloist вместе с Джарвисом. Soloist — отдельный проект (Horohi/soloist-station-lite, движок принадлежит
// Spotify AB): мы не вшиваем его в установщик, а скачиваем официальный релиз с GitHub автора и запускаем его install.sh.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	soloistReleaseAPI = "https://api.github.com/repos/Horohi/soloist-station-lite/releases/latest"
	soloistAsset      = "soloist-station-lite.tar.gz"
	soloistTmp        = "/data/local/tmp/soloist-station-lite"
	soloistMaxGz      = 150 << 20
	soloistMaxTar     = 400 << 20
)

type SoloistOpts struct{ Key, Name, ClientID, ClientSecret string }

func (o SoloistOpts) enabled() bool { return strings.TrimSpace(o.Key) != "" }

// validate: значения попадают в команду на колонке, поэтому оставляем только безопасные символы.
func (o SoloistOpts) validate() error {
	for _, v := range []string{o.Key, o.Name, o.ClientID, o.ClientSecret} {
		if len(v) > 200 {
			return errors.New("слишком длинное значение в настройках Spotify")
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f || strings.ContainsRune("\"'\\$`", r) {
				return errors.New("в настройках Spotify недопустимые символы (кавычки, $, обратный слэш)")
			}
		}
	}
	return nil
}

// fetchSoloist скачивает последний релиз Soloist и возвращает распакованный из gzip tar и тег релиза.
// SHA-256 сверяется, если GitHub отдаёт digest файла.
func fetchSoloist(ctx context.Context, c *http.Client, api string) (tar []byte, tag string, err error) {
	get := func(url string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "JarvisSetup/"+version)
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
		}
		return resp, nil
	}
	resp, err := get(api)
	if err != nil {
		return nil, "", fmt.Errorf("не удалось получить релиз Soloist: %w", err)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel)
	resp.Body.Close()
	if err != nil {
		return nil, "", fmt.Errorf("не понял ответ GitHub: %w", err)
	}
	url, digest := "", ""
	for _, a := range rel.Assets {
		if a.Name == soloistAsset {
			url, digest = a.URL, strings.ToLower(a.Digest)
		}
	}
	if url == "" {
		return nil, "", fmt.Errorf("в релизе %s нет файла %s", rel.Tag, soloistAsset)
	}
	body, err := get(url)
	if err != nil {
		return nil, "", fmt.Errorf("не удалось скачать Soloist: %w", err)
	}
	defer body.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(body.Body, soloistMaxGz+1))
	if err != nil {
		return nil, "", fmt.Errorf("загрузка прервалась: %w", err)
	}
	if len(raw) > soloistMaxGz {
		return nil, "", errors.New("архив Soloist слишком большой")
	}
	if strings.HasPrefix(digest, "sha256:") {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != strings.TrimPrefix(digest, "sha256:") {
			return nil, "", errors.New("контрольная сумма SHA-256 архива Soloist не сошлась — файл повреждён")
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("архив Soloist не открылся: %w", err)
	}
	tar, err = io.ReadAll(io.LimitReader(zr, soloistMaxTar+1))
	if err != nil || len(tar) > soloistMaxTar {
		return nil, "", errors.New("архив Soloist не распаковался или слишком велик")
	}
	return tar, rel.Tag, nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// soloistScript — команда для колонки: запустить install.sh из dir. Значения передаются в кавычках; на вопросы
// установщика отвечают Client ID/Secret (если заданы), дальше пустые ответы — то есть значения по умолчанию.
// Вывод уходит в файл: установщик запускает службу, и она держала бы канал связи с колонкой открытым.
func soloistScript(dir, log string, o SoloistOpts) string {
	return "cd " + shq(dir) + " && { printf '%s\\n%s\\n' " + shq(o.ClientID) + " " + shq(o.ClientSecret) +
		"; yes ''; } | sh ./install.sh " + shq(o.Key) + " " + shq(o.Name) + " >" + shq(log) + " 2>&1"
}

// installSoloist ставит Soloist на колонку по ADB. Ошибка не отменяет установку Джарвиса — её только показывают.
func installSoloist(j *job, a *ADB, freeKB func() int, o SoloistOpts) error {
	if err := o.validate(); err != nil {
		return err
	}
	j.log("Spotify: скачиваю Soloist с GitHub…")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tar, tag, err := fetchSoloist(ctx, http.DefaultClient, soloistReleaseAPI)
	if err != nil {
		return err
	}
	if kb := freeKB(); kb > 0 && kb < len(tar)*2/1024+45000 {
		return fmt.Errorf("на колонке мало места для Soloist: свободно %d МБ, нужно около %d МБ", kb/1024, (len(tar)*2/1024+45000)/1024)
	}
	j.log("Spotify: копирую Soloist %s (%d МБ)…", tag, len(tar)>>20)
	sh := func(cmd string, t time.Duration) (string, error) {
		out, rc, err := a.Shell(cmd, t)
		if err != nil {
			return out, err
		}
		if rc != 0 {
			return out, fmt.Errorf("команда на колонке не прошла (%d): %s", rc, strings.TrimSpace(out))
		}
		return out, nil
	}
	_, _ = sh("rm -rf "+soloistTmp+" /data/local/tmp/soloist.tar; mkdir -p "+soloistTmp, 30*time.Second)
	if err := a.Push(tar, "/data/local/tmp/soloist.tar", 0o644, nil); err != nil {
		return err
	}
	if out, err := sh("tar -xf /data/local/tmp/soloist.tar -C "+soloistTmp+" && rm -f /data/local/tmp/soloist.tar", 120*time.Second); err != nil {
		return fmt.Errorf("на колонке не распаковался архив Soloist: %s", strings.TrimSpace(out))
	}
	dir, err := sh("d="+soloistTmp+"; [ -f $d/install.sh ] || d=$(dirname $(ls $d/*/install.sh 2>/dev/null | head -n 1)); echo $d", 15*time.Second)
	dir = strings.TrimSpace(dir)
	if err != nil || dir == "" || dir == "." {
		return errors.New("в архиве Soloist нет install.sh")
	}
	j.log("Spotify: запускаю установщик Soloist (до пары минут)…")
	log := soloistTmp + "/install.log"
	_, err = sh(soloistScript(dir, log, o), 6*time.Minute)
	tail, _ := sh("tail -n 12 "+shq(log)+" 2>/dev/null", 10*time.Second)
	tail = strings.TrimSpace(tail)
	_, _ = sh("rm -rf "+soloistTmp, 30*time.Second)
	if err != nil {
		return fmt.Errorf("установщик Soloist завершился ошибкой: %s", tail)
	}
	j.log("Spotify: Soloist установлен. Вывод установщика:\n%s", tail)
	return nil
}
