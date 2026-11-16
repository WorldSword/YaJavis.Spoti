package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Установка Soloist с GitHub автора (Horohi/soloist-station-lite). Движок Soloist принадлежит Spotify AB
// и не входит в YaJavis: мы только скачиваем официальный релиз по нажатию кнопки и запускаем его install.sh.
const (
	soloistRepoAPI      = "https://api.github.com/repos/Horohi/soloist-station-lite/releases/latest"
	soloistArchiveName  = "soloist-station-lite.tar.gz"
	soloistMaxDownload  = 150 << 20
	soloistMaxExtracted = 400 << 20
)

type soloistInstallOpts struct{ APIKey, DeviceName, ClientID, ClientSecret string }

type SoloistInstaller struct {
	api    string
	arch   string
	tmp    string
	get    func(ctx context.Context, url string) (*http.Response, error)
	run    spotifyExec
	OnDone func() // установка удалась: Джарвис перезапускается, чтобы подхватить Soloist

	mu    sync.Mutex
	state string // "" | running | done | error
	step  string
	err   string
}

func newSoloistInstaller(run spotifyExec) *SoloistInstaller {
	tmp := "/data/local/tmp"
	if st, err := os.Stat(tmp); err != nil || !st.IsDir() {
		tmp = os.TempDir()
	}
	return &SoloistInstaller{api: soloistRepoAPI, arch: runtime.GOARCH, tmp: tmp, run: run,
		get: func(ctx context.Context, url string) (*http.Response, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			req.Header.Set("User-Agent", "jarvisd/"+version)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				return nil, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
			}
			return resp, nil
		}}
}

func (i *SoloistInstaller) Status() map[string]any {
	if i == nil {
		return map[string]any{"state": ""}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return map[string]any{"state": i.state, "step": i.step, "error": i.err}
}

func (i *SoloistInstaller) setStep(s string) {
	i.mu.Lock()
	i.step = s
	i.mu.Unlock()
}

// Start проверяет вход и запускает установку в фоне.
func (i *SoloistInstaller) Start(o soloistInstallOpts) error {
	if i == nil || i.run == nil {
		return errors.New("установка недоступна")
	}
	if o.APIKey == "" {
		return errors.New("нужен API-ключ Soloist")
	}
	for _, v := range []string{o.APIKey, o.DeviceName, o.ClientID, o.ClientSecret} {
		if !validSoloistValue(v) {
			return errors.New("недопустимые символы в настройках")
		}
	}
	if i.arch != "arm64" {
		return errors.New("Soloist ставится только на колонку с aarch64 (arm64), здесь " + i.arch)
	}
	i.mu.Lock()
	if i.state == "running" {
		i.mu.Unlock()
		return errors.New("установка уже идёт")
	}
	i.state, i.step, i.err = "running", "ищу свежий релиз", ""
	i.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		err := i.install(ctx, o)
		i.mu.Lock()
		if err != nil {
			i.state, i.err = "error", err.Error()
		} else {
			i.state, i.step = "done", "готово"
		}
		i.mu.Unlock()
		if err == nil && i.OnDone != nil {
			i.OnDone()
		}
	}()
	return nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func (i *SoloistInstaller) install(ctx context.Context, o soloistInstallOpts) error {
	resp, err := i.get(ctx, i.api)
	if err != nil {
		return fmt.Errorf("не удалось получить релиз Soloist: %w", err)
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
		return fmt.Errorf("не понял ответ GitHub: %w", err)
	}
	url, digest := "", ""
	for _, a := range rel.Assets {
		if a.Name == soloistArchiveName {
			url, digest = a.URL, a.Digest
			break
		}
	}
	if url == "" {
		return fmt.Errorf("в релизе %s нет файла %s", rel.Tag, soloistArchiveName)
	}

	work, err := os.MkdirTemp(i.tmp, "soloist-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	i.setStep("скачиваю Soloist " + rel.Tag)
	archive := filepath.Join(work, soloistArchiveName)
	sum, err := i.download(ctx, url, archive)
	if err != nil {
		return err
	}
	if want := strings.TrimPrefix(strings.ToLower(digest), "sha256:"); strings.HasPrefix(strings.ToLower(digest), "sha256:") && want != sum {
		return errors.New("контрольная сумма SHA-256 не сошлась — файл повреждён, установка отменена")
	}

	i.setStep("распаковываю")
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	src := filepath.Join(work, "src")
	err = extractTarGz(f, src, soloistMaxExtracted)
	f.Close()
	if err != nil {
		return fmt.Errorf("архив Soloist не распаковался: %w", err)
	}
	dir, err := findInstallScript(src)
	if err != nil {
		return err
	}

	i.setStep("запускаю установщик Soloist")
	rctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	// ключи и имя идут отдельными аргументами, а не в тексте команды; на вопросы установщика отвечают
	// Client ID/Secret (по желанию), дальше — пустые ответы, то есть значения по умолчанию.
	const script = `cd "$1" && { printf '%s\n%s\n' "$4" "$5"; yes ''; } | sh ./install.sh "$2" "$3"`
	out, err := i.run(rctx, "sh", "-c", script, "sh", dir, o.APIKey, o.DeviceName, o.ClientID, o.ClientSecret)
	if err != nil {
		tail := strings.TrimSpace(ansiRe.ReplaceAllString(string(out), ""))
		if r := []rune(tail); len(r) > 400 {
			tail = "…" + string(r[len(r)-400:])
		}
		return fmt.Errorf("установщик Soloist завершился ошибкой: %s", tail)
	}
	return nil
}

// download сохраняет файл и возвращает SHA-256 в hex.
func (i *SoloistInstaller) download(ctx context.Context, url, dst string) (string, error) {
	resp, err := i.get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("не удалось скачать Soloist: %w", err)
	}
	defer resp.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, soloistMaxDownload+1))
	if err != nil {
		return "", fmt.Errorf("загрузка прервалась: %w", err)
	}
	if n > soloistMaxDownload {
		return "", errors.New("архив слишком большой")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findInstallScript — папка с install.sh: корень архива или его единственная вложенная папка.
func findInstallScript(root string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, "install.sh")); err == nil {
		return root, nil
	}
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "install.sh")); err == nil {
				return filepath.Join(root, e.Name()), nil
			}
		}
	}
	return "", errors.New("в архиве Soloist нет install.sh")
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// extractTarGz — безопасная распаковка: ни абсолютных путей, ни «..», ни ссылок наружу, общий размер ограничен.
func extractTarGz(r io.Reader, dst string, maxBytes int64) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	realDst, err := filepath.EvalSymlinks(dst)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(name) || filepath.IsAbs(h.Name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("недопустимый путь в архиве: %q", h.Name)
		}
		target := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if real, err := filepath.EvalSymlinks(filepath.Dir(target)); err != nil || !within(realDst, real) {
				return fmt.Errorf("недопустимый путь в архиве: %q", h.Name)
			}
			total += h.Size
			if total > maxBytes {
				return errors.New("архив слишком большой после распаковки")
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode&0o777)|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			f.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || !within(dst, filepath.Join(filepath.Dir(target), h.Linkname)) {
				return fmt.Errorf("ссылка выходит за папку: %q → %q", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		default: // жёсткие ссылки, устройства и прочее нам не нужны
		}
	}
}
