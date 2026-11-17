package main

// Обновление jarvisd из настроек: с GitHub (последний релиз) или файлом с компьютера.
// Новый файл скачивается в /tmp (на /data нет места на две копии), проверяется (sha256 и запуск
// «jarvisd version»), кладётся в /tmp/jarvisd/jarvisd.next, и программа выходит — jarvisd.sh
// подменяет файл и запускает новую версию.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const updateNext = "/tmp/jarvisd/jarvisd.next"

// defaultRepo — репозиторий релизов, зашитый при сборке (build.sh REPO=owner/name).
var defaultRepo = "newfpv/YaJavis"

// repo — откуда брать обновления: из настроек, иначе зашитый при сборке.
func (u *Updater) repo() string {
	if r := u.store.Get().UpdateRepo; r != "" {
		return r
	}
	return defaultRepo
}

type Updater struct {
	store  *Store
	client func() *http.Client
	idle   func() bool // никто не говорит и музыка не играет

	mu      sync.Mutex
	latest  string // версия последнего релиза
	notes   string
	asset   string // ссылка на файл для этой колонки
	sum     string // его sha256 из SHA256SUMS (если есть)
	checked time.Time
	err     string
	busy    string // что делаем сейчас

	// prepare — перед заменой ядра поставить плагины, нужные новой версии (EnsureFor). Ошибка — обновление
	// откладывается: колонка остаётся на рабочей версии, а не просыпается без нужной функции.
	prepare func(ctx context.Context, tag string) error
}

func assetName() string {
	if runtime.GOARCH == "arm" {
		return "jarvisd-armv7"
	}
	return "jarvisd-" + runtime.GOARCH
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// newer — версия a новее b («v0.10.1» > «0.9.7»).
func newer(a, b string) bool {
	pa, pb := verParts(a), verParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func verParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, p := range strings.SplitN(v, ".", 3) {
		// берём ведущие цифры: «70-spotify1» и «70-rc1» — это 70
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		out[i], _ = strconv.Atoi(p[:end])
	}
	return out
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "jarvisd/"+version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
	}
	return resp, nil
}

// Check — что лежит в последнем релизе на GitHub.
func (u *Updater) Check(ctx context.Context) error {
	repo := u.repo()
	if !repoRe.MatchString(repo) {
		return errors.New("не задан репозиторий GitHub (вида owner/name)")
	}
	err := u.check(ctx, repo)
	u.mu.Lock()
	u.checked, u.err = time.Now(), ""
	if err != nil {
		u.err = err.Error()
	}
	u.mu.Unlock()
	return err
}

func (u *Updater) check(ctx context.Context, repo string) error {
	resp, err := u.get(ctx, "https://api.github.com/repos/"+repo+"/releases/latest")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var rel struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return err
	}
	asset, sums := "", ""
	for _, a := range rel.Assets {
		switch a.Name {
		case assetName():
			asset = a.URL
		case "SHA256SUMS":
			sums = a.URL
		}
	}
	if asset == "" {
		return fmt.Errorf("в релизе %s нет файла %s", rel.Tag, assetName())
	}
	if sums == "" {
		return errors.New("в релизе нет SHA256SUMS — обновление не будет установлено")
	}
	sum := ""
	if sums != "" {
		if r, err := u.get(ctx, sums); err == nil {
			sc := bufio.NewScanner(io.LimitReader(r.Body, 1<<20))
			for sc.Scan() {
				if f := strings.Fields(sc.Text()); len(f) == 2 && strings.TrimPrefix(f[1], "*") == assetName() {
					sum = strings.ToLower(f[0])
				}
			}
			r.Body.Close()
		}
	}
	if raw, err := hex.DecodeString(sum); err != nil || len(raw) != sha256.Size {
		return errors.New("не удалось получить SHA-256 файла обновления из SHA256SUMS")
	}
	notes := rel.Body
	if len(notes) > 2000 {
		notes = notes[:2000] + "…"
	}
	u.mu.Lock()
	u.latest, u.notes, u.asset, u.sum = rel.Tag, notes, asset, sum
	u.mu.Unlock()
	return nil
}

func (u *Updater) Available() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.latest != "" && newer(u.latest, version)
}

func (u *Updater) setBusy(s string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if s != "" && u.busy != "" {
		return false
	}
	u.busy = s
	return true
}

// InstallLatest — скачать релиз с GitHub и поставить.
func (u *Updater) InstallLatest(ctx context.Context) error {
	u.mu.Lock()
	asset, sum, latest := u.asset, u.sum, u.latest
	u.mu.Unlock()
	if asset == "" || !newer(latest, version) {
		return errors.New("обновлений нет")
	}
	if !u.setBusy("скачиваю " + latest) {
		return errors.New("обновление уже идёт")
	}
	defer u.setBusy("")
	if u.prepare != nil {
		if err := u.prepare(ctx, latest); err != nil {
			return fmt.Errorf("обновление отложено: %w", err)
		}
	}
	resp, err := u.get(ctx, asset)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return u.install(resp.Body, sum)
}

// InstallFrom — поставить файл, загруженный из браузера.
func (u *Updater) InstallFrom(r io.Reader) error {
	if !u.setBusy("ставлю файл с компьютера") {
		return errors.New("обновление уже идёт")
	}
	defer u.setBusy("")
	return u.install(r, "")
}

func (u *Updater) install(r io.Reader, sum string) error {
	_ = os.MkdirAll(filepath.Dir(updateNext), 0o755)
	tmp := updateNext + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, 80<<20))
	f.Close()
	fail := func(e error) error { _ = os.Remove(tmp); return e }
	if err != nil {
		return fail(err)
	}
	if n < 1<<20 {
		return fail(errors.New("файл слишком маленький — это не jarvisd"))
	}
	if sum != "" && hex.EncodeToString(h.Sum(nil)) != sum {
		return fail(errors.New("контрольная сумма не сошлась — файл повреждён"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp, "version").Output()
	got := strings.TrimSpace(string(out))
	if err != nil || got == "" {
		return fail(errors.New("файл не запускается на этой колонке (не та архитектура?)"))
	}
	if err := os.Rename(tmp, updateNext); err != nil {
		return fail(err)
	}
	log.Printf("обновление: %s → %s, перезапуск", version, got)
	restartSoon()
	return nil
}

// Run — проверка раз в 6 часов; если разрешено — ставит сама ночью, когда колонка молчит.
func (u *Updater) Run() {
	time.Sleep(2 * time.Minute)
	for {
		s := u.store.Get()
		if s.UpdateCheck && repoRe.MatchString(u.repo()) {
			u.mu.Lock()
			due := time.Since(u.checked) > 6*time.Hour
			u.mu.Unlock()
			if due {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if err := u.Check(ctx); err != nil {
					log.Printf("обновление: проверка: %v", err)
				}
				cancel()
			}
			h := s.Now().Hour()
			if s.UpdateAuto && u.Available() && h >= 3 && h < 6 && u.idle() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				if err := u.InstallLatest(ctx); err != nil {
					log.Printf("обновление: %v", err)
				}
				cancel()
			}
		}
		time.Sleep(20 * time.Minute)
	}
}

// ---- веб

func (w *Web) getUpdate(rw http.ResponseWriter, r *http.Request) {
	u, s := w.updater, w.store.Get()
	u.mu.Lock()
	defer u.mu.Unlock()
	checked := ""
	if !u.checked.IsZero() {
		checked = u.checked.In(s.Now().Location()).Format("02.01 15:04")
	}
	writeJSON(rw, 200, map[string]any{"version": version, "repo": u.repo(), "check": s.UpdateCheck, "auto": s.UpdateAuto,
		"latest": u.latest, "notes": u.notes, "available": u.latest != "" && newer(u.latest, version),
		"checked": checked, "error": u.err, "busy": u.busy, "asset": assetName(), "stats": !s.NoStats})
}

func (w *Web) putUpdateSettings(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo  *string `json:"repo"`
		Check *bool   `json:"check"`
		Auto  *bool   `json:"auto"`
		Stats *bool   `json:"stats"`
	}
	if readJSON(r, &req) != nil {
		bad(rw, "пустой запрос")
		return
	}
	err := w.store.Update(func(s *Settings) error {
		if req.Repo != nil {
			repo := strings.TrimSpace(*req.Repo)
			repo = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "github.com/"), ".git")
			if repo != "" && !repoRe.MatchString(repo) {
				return errors.New("репозиторий — вида owner/name")
			}
			s.UpdateRepo = repo
		}
		if req.Check != nil {
			s.UpdateCheck = *req.Check
		}
		if req.Auto != nil {
			s.UpdateAuto = *req.Auto
		}
		if req.Stats != nil {
			s.NoStats = !*req.Stats
		}
		return nil
	})
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Web) checkUpdate(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := w.updater.Check(ctx); err != nil {
		bad(rw, err.Error())
		return
	}
	w.getUpdate(rw, r)
}

func (w *Web) installUpdate(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := w.updater.InstallLatest(ctx); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Web) uploadUpdate(rw http.ResponseWriter, r *http.Request) {
	if err := w.updater.InstallFrom(r.Body); err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}
