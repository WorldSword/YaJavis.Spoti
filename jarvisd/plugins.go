package main

// Плагины. Два вида:
//   - js — лёгкий: plugin.json + скрипт, выполняется встроенным goja (jsplugin.go). Работает на любой колонке,
//     без сборки, весит килобайты. Инструменты для модели, свои быстрые команды, запросы в интернет.
//   - native — отдельная программа под архитектуру колонки (arm64 / armv7), говорит с jarvisd строками JSON
//     через stdin/stdout (nativeplugin.go). Для тяжёлого: голосовые движки, музыка, VPN.
// Лежат в <data>/plugins/<id>/, включённость и настройки — в <data>/plugins.json (там могут быть ключи: 0600).
// Ставятся из релиза GitHub (owner/repo → jarvis-plugin[-arch].zip) или архивом с компьютера.
// Каталог: официальные — plugins/index.json в репозитории Джарвиса, остальные — репозитории GitHub с темой
// yajarvis-plugin; они помечаются как неофициальные. Формат и API — docs/PLUGINS.md.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// адреса GitHub (тесты подменяют на свой сервер)
var githubAPI, githubRaw = "https://api.github.com", "https://raw.githubusercontent.com"

const (
	pluginTopic     = "yajarvis-plugin"
	pluginZipMax    = 24 << 20 // архив плагина
	pluginUnpackMax = 48 << 20 // распакованный
	pluginAPI       = 1        // версия протокола/API плагинов
)

var pluginIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)

// PluginCategory — раздел каталога. Автор плагина выбирает ровно один (поле category в plugin.json);
// на GitHub — тема репозитория yajarvis-<id>, например yajarvis-home. Порядок — как в панели, «Другое» последним.
type PluginCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Hint string `json:"hint"`
}

var pluginCategories = []PluginCategory{
	{"assistant", "Ассистенты и голоса", "голосовые движки, нейросети, голоса"},
	{"home", "Умный дом", "свет, розетки, датчики, сценарии"},
	{"media", "Музыка и видео", "музыка, радио, подкасты, видео"},
	{"info", "Погода и справки", "погода, курсы, новости, транспорт"},
	{"productivity", "Дела и заметки", "списки, напоминания, календари, почта"},
	{"dev", "Кодинг и IT", "серверы, GitHub, мониторинг, сеть"},
	{"fun", "Игры и развлечения", "игры, шутки, викторины, случайности"},
	{"system", "Колонка и система", "подсветка, кнопки, слово активации, VPN"},
	{"other", "Другое", "всё, что не подошло выше"},
}

func validCategory(id string) bool {
	for _, c := range pluginCategories {
		if c.ID == id {
			return true
		}
	}
	return false
}

func categoryIDs() string {
	ids := make([]string, len(pluginCategories))
	for i, c := range pluginCategories {
		ids[i] = c.ID
	}
	return strings.Join(ids, ", ")
}

type PluginSetting struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | password | number | bool | select | textarea
	Hint     string   `json:"hint,omitempty"`
	Options  []string `json:"options,omitempty"`
	Default  any      `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
}

type PluginManifest struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Author      string          `json:"author,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	Kind        string          `json:"kind"`           // js | native
	Main        string          `json:"main"`           // js: файл скрипта; native: программа, {arch} → arm64 / armv7
	API         int             `json:"api,omitempty"`  // версия API, под которую написан (сейчас 1)
	Icon        string          `json:"icon,omitempty"` // имя иконки панели: puzzle, cloud, music, home, mic, bolt…
	Category    string          `json:"category"`       // раздел каталога: см. pluginCategories
	Provides    []string        `json:"provides,omitempty"`
	Settings    []PluginSetting `json:"settings,omitempty"`
	MinCore     string          `json:"min_core,omitempty"`
}

type pluginState struct {
	Removed  bool           `json:"removed,omitempty"` // официальный плагин удалили руками — сами обратно не ставим
	Enabled  bool           `json:"enabled"`
	Source   string         `json:"source"` // owner/repo или file
	Official bool           `json:"official"`
	At       time.Time      `json:"installed_at"`
	Settings map[string]any `json:"settings,omitempty"`
}

// pluginRuntime — запущенный плагин (JS или отдельная программа).
type pluginRuntime interface {
	Tools() []any
	CallTool(ctx context.Context, name, args string) (string, bool)
	Command(ctx context.Context, text string) (bool, string)
	Configure(settings map[string]any)
	Status() string
	Stop()
}

type Plugin struct {
	Manifest PluginManifest
	Dir      string
	rt       pluginRuntime
	err      string
}

// PluginHooks — что ядро даёт плагинам.
type PluginHooks struct {
	Request func(id, method string, params json.RawMessage) (any, error) // scoped core services for native providers
	Say     func(text string)                                            // сказать голосом активного ассистента
	Alice   func(text string, silent bool) error                         // запрос штатной Алисе
	Info    func() map[string]any                                        // город, комната, язык, версия
	Client  func(timeout time.Duration) *http.Client                     // интернет для плагинов (через VPN, если отмечено)
}

type PluginHost struct {
	dataDir string
	hooks   PluginHooks
	github  func() *http.Client
	index   string // ссылка на plugins/index.json официального репозитория

	mu        sync.Mutex
	installMu sync.Mutex
	plugins   map[string]*Plugin
	state     map[string]*pluginState

	catMu  sync.Mutex
	cat    []CatalogItem
	catAt  time.Time
	catErr string
}

// pluginHost — плагины этой колонки (main); nil в тестах и утилитах.
var pluginHost *PluginHost

func pluginArch() string {
	if runtime.GOARCH == "arm" {
		return "armv7"
	}
	return runtime.GOARCH
}

func NewPluginHost(dataDir string, hooks PluginHooks, github func() *http.Client) *PluginHost {
	return &PluginHost{dataDir: dataDir, hooks: hooks, github: github, plugins: map[string]*Plugin{}, state: map[string]*pluginState{},
		index: githubRaw + "/" + defaultRepo + "/main/plugins/index.json"}
}

func (h *PluginHost) dir() string       { return filepath.Join(h.dataDir, "plugins") }
func (h *PluginHost) statePath() string { return filepath.Join(h.dataDir, "plugins.json") }

// Load — прочитать установленные плагины и запустить включённые.
func (h *PluginHost) Load() {
	state := map[string]*pluginState{}
	if b, err := os.ReadFile(h.statePath()); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	h.mu.Lock()
	for id, st := range state {
		if st != nil {
			h.state[id] = st
		}
	}
	h.mu.Unlock()
	ents, _ := os.ReadDir(h.dir())
	for _, e := range ents {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".old") {
			continue
		}
		dir := filepath.Join(h.dir(), e.Name())
		m, err := readManifest(dir)
		broken := ""
		if err == nil && m.ID != e.Name() {
			err = fmt.Errorf("id в plugin.json (%s) не совпадает с папкой", m.ID)
		}
		if err != nil { // сломанный плагин показываем в панели, чтобы его можно было удалить, но не запускаем
			if !pluginIDRe.MatchString(e.Name()) {
				log.Printf("плагины: папка %s пропущена: %v", e.Name(), err)
				continue
			}
			broken = "плагин повреждён: " + err.Error()
			log.Printf("плагины: %s: %s", e.Name(), broken)
			name := m.Name
			if strings.TrimSpace(name) == "" {
				name = e.Name()
			}
			m = PluginManifest{ID: e.Name(), Name: name, Version: m.Version, Description: m.Description, Kind: m.Kind, Category: "other"}
		}
		h.mu.Lock()
		p := &Plugin{Manifest: m, Dir: dir, err: broken}
		h.plugins[m.ID] = p
		st := h.stateLocked(m.ID)
		later := broken == "" && needsNewerCore(m) // поставлен перед обновлением ядра, которое ещё не применилось
		if later {
			p.err = "заработает после обновления Джарвиса до " + m.MinCore
		}
		enabled := st.Enabled
		h.mu.Unlock()
		if enabled && !later && broken == "" {
			h.start(p)
		}
	}
	h.mu.Lock()
	for id, st := range h.state { // папку плагина удалили — забыть (кроме отметки «удалён руками»)
		if h.plugins[id] == nil && !st.Removed {
			delete(h.state, id)
		}
	}
	h.saveLocked()
	h.mu.Unlock()
}

func (h *PluginHost) stateLocked(id string) *pluginState {
	st := h.state[id]
	if st == nil {
		st = &pluginState{Enabled: true, Source: "file", At: time.Now()}
		h.state[id] = st
	}
	return st
}

func (h *PluginHost) saveLocked() {
	b, _ := json.MarshalIndent(h.state, "", "  ")
	tmp := h.statePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, h.statePath())
	}
}

func readManifest(dir string) (PluginManifest, error) {
	var m PluginManifest
	b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("plugin.json: %w", err)
	}
	return m, validManifest(m)
}

func validManifest(m PluginManifest) error {
	switch {
	case !pluginIDRe.MatchString(m.ID):
		return errors.New("id плагина: латиница в нижнем регистре, цифры и дефис, 2–32 символа")
	case strings.TrimSpace(m.Name) == "":
		return errors.New("у плагина нет name")
	case m.Kind != "js" && m.Kind != "native":
		return errors.New(`kind плагина — "js" или "native"`)
	case !validCategory(m.Category):
		return fmt.Errorf("category плагина — одна из: %s", categoryIDs())
	case slices.Contains(m.Provides, "vpn") && m.Kind != "native":
		return errors.New("провайдер VPN должен быть нативным плагином")
	case m.Kind != "native" && (slices.Contains(m.Provides, "homeassistant") || slices.Contains(m.Provides, "xiaomi") || slices.Contains(m.Provides, "smartlife") || slices.Contains(m.Provides, "yandexhome")):
		return errors.New("провайдер интеграции должен быть нативным плагином")
	case m.Main == "" || strings.Contains(m.Main, "..") || path.IsAbs(m.Main) || strings.ContainsAny(m.Main, `\`):
		return errors.New("main плагина — имя файла внутри архива")
	case m.API > pluginAPI:
		return fmt.Errorf("плагин написан под API %d, а этот Джарвис знает %d — обновите Джарвиса", m.API, pluginAPI)
	}
	return nil
}

// needsNewerCore — плагин написан для более новой версии Джарвиса.
func needsNewerCore(m PluginManifest) bool {
	return m.MinCore != "" && version != "dev" && newer(m.MinCore, version)
}

// settings — значения настроек плагина с умолчаниями из манифеста.
func (h *PluginHost) settingsLocked(p *Plugin) map[string]any {
	out := map[string]any{}
	for _, s := range p.Manifest.Settings {
		if s.Default != nil {
			out[s.Key] = s.Default
		}
	}
	for k, v := range h.stateLocked(p.Manifest.ID).Settings {
		out[k] = v
	}
	return out
}

func (h *PluginHost) start(p *Plugin) {
	h.mu.Lock()
	settings := h.settingsLocked(p)
	if needsNewerCore(p.Manifest) {
		p.err = "заработает после обновления Джарвиса до " + p.Manifest.MinCore
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	var rt pluginRuntime
	var err error
	switch p.Manifest.Kind {
	case "js":
		rt, err = startJSPlugin(h, p, settings)
	case "native":
		rt, err = startNativePlugin(h, p, settings)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p.rt, p.err = rt, ""
	if err != nil {
		p.err = err.Error()
		log.Printf("плагины: %s не запустился: %v", p.Manifest.ID, err)
		return
	}
	log.Printf("плагины: %s %s запущен", p.Manifest.ID, p.Manifest.Version)
}

func (h *PluginHost) stop(p *Plugin) {
	h.mu.Lock()
	rt := p.rt
	p.rt = nil
	h.mu.Unlock()
	if rt != nil {
		rt.Stop()
	}
}

func (h *PluginHost) running() []pluginRuntime {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.plugins))
	for id := range h.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []pluginRuntime
	for _, id := range ids {
		if rt := h.plugins[id].rt; rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

// Tools — инструменты всех запущенных плагинов (для модели).
func (h *PluginHost) Tools() []any {
	if h == nil {
		return nil
	}
	var out []any
	seen := map[string]bool{}
	for _, rt := range h.running() { // по алфавиту id: при одинаковых именах побеждает первый — он же и вызывается
		for _, t := range rt.Tools() {
			def, _ := t.(map[string]any)
			name, _ := def["name"].(string)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, t)
		}
	}
	return out
}

func (h *PluginHost) CallTool(ctx context.Context, name, args string) (string, bool) {
	if h == nil {
		return "", false
	}
	for _, rt := range h.running() {
		if res, ok := rt.CallTool(ctx, name, args); ok {
			return res, true
		}
	}
	return "", false
}

// Command — быстрые команды плагинов: фраза разобрана без модели.
func (h *PluginHost) Command(ctx context.Context, text string) (bool, string) {
	if h == nil {
		return false, ""
	}
	for _, rt := range h.running() {
		if ok, answer := rt.Command(ctx, text); ok {
			return true, answer
		}
	}
	return false, ""
}

// ---------------------------------------------------------------- установка

// Install — поставить (или обновить) плагин из zip. source — owner/repo или file.
func (h *PluginHost) Install(raw []byte, source string, official bool) (PluginManifest, error) {
	return h.install(raw, source, official, false)
}

// install; forNext — ставим перед обновлением ядра: плагину можно требовать более новую версию Джарвиса,
// он запустится уже новой версией.
func (h *PluginHost) install(raw []byte, source string, official, forNext bool) (PluginManifest, error) {
	h.installMu.Lock() // две установки сразу (двойной клик, мастер и страница) делили бы одну временную папку
	defer h.installMu.Unlock()
	files, m, err := unpackPlugin(raw)
	if err != nil {
		return m, err
	}
	later := needsNewerCore(m)
	if later && !forNext {
		return m, fmt.Errorf("плагину нужен Джарвис %s или новее — сначала обновите Джарвиса", m.MinCore)
	}
	if m.Kind == "native" {
		bin := strings.ReplaceAll(m.Main, "{arch}", pluginArch())
		if _, ok := files[bin]; !ok {
			return m, fmt.Errorf("в плагине нет программы для этой колонки (%s)", bin)
		}
	}
	total := 0
	for _, b := range files {
		total += len(b)
	}
	if free, _ := diskSpace(h.dataDir); free > 0 && free < total>>20+3 {
		return m, fmt.Errorf("мало места на колонке: свободно %d МБ, плагину нужно %d МБ", free, total>>20+1)
	}
	dst := filepath.Join(h.dir(), m.ID)
	tmp := dst + ".tmp"
	_ = os.RemoveAll(tmp)
	for name, b := range files {
		p := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return m, err
		}
		mode := os.FileMode(0o644)
		if m.Kind == "native" && (name == strings.ReplaceAll(m.Main, "{arch}", "arm64") || name == strings.ReplaceAll(m.Main, "{arch}", "armv7") ||
			name == strings.ReplaceAll(m.Main, "{arch}", pluginArch())) {
			mode = 0o755
		}
		if err := os.WriteFile(p, b, mode); err != nil {
			_ = os.RemoveAll(tmp)
			return m, err
		}
	}
	h.mu.Lock()
	old := h.plugins[m.ID]
	h.mu.Unlock()
	if old != nil {
		h.stop(old)
		// данные плагина (data.json, data/) переживают обновление
		for _, keep := range []string{"data.json", "data"} {
			if _, err := os.Stat(filepath.Join(dst, keep)); err == nil {
				_ = os.RemoveAll(filepath.Join(tmp, keep))
				_ = os.Rename(filepath.Join(dst, keep), filepath.Join(tmp, keep))
			}
		}
	}
	bak := dst + ".old"
	_ = os.RemoveAll(bak)
	hadOld := os.Rename(dst, bak) == nil
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		if hadOld {
			_ = os.Rename(bak, dst) // вернуть прежнюю версию
			if old != nil {
				h.start(old)
			}
		}
		return m, err
	}
	_ = os.RemoveAll(bak)
	p := &Plugin{Manifest: m, Dir: dst}
	h.mu.Lock()
	h.plugins[m.ID] = p
	st := h.stateLocked(m.ID)
	st.Source, st.Official, st.At = source, official, time.Now()
	if old == nil {
		st.Enabled = true
	}
	st.Removed = false
	enabled := st.Enabled
	h.saveLocked()
	if later {
		p.err = "заработает после обновления Джарвиса до " + m.MinCore
	}
	h.mu.Unlock()
	if enabled && !later {
		h.start(p)
	}
	log.Printf("плагины: установлен %s %s (%s)", m.ID, m.Version, source)
	return m, nil
}

// unpackPlugin — разобрать и проверить архив: только обычные файлы, без выхода за папку, разумный размер.
// Архив может лежать как есть или в одной папке верхнего уровня (как его делает GitHub).
func unpackPlugin(raw []byte) (map[string][]byte, PluginManifest, error) {
	var m PluginManifest
	if len(raw) > pluginZipMax {
		return nil, m, fmt.Errorf("архив плагина больше %d МБ", pluginZipMax>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, m, errors.New("это не zip-архив плагина")
	}
	files := map[string][]byte{}
	total := 0
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if f.FileInfo().IsDir() || strings.HasPrefix(path.Base(name), "__MACOSX") || strings.Contains(name, "/__MACOSX/") {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, m, fmt.Errorf("в архиве не обычный файл: %s", name)
		}
		clean := path.Clean(name)
		if clean != name || path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
			return nil, m, fmt.Errorf("в архиве недопустимый путь: %s", name)
		}
		total += int(f.UncompressedSize64)
		if total > pluginUnpackMax {
			return nil, m, fmt.Errorf("плагин больше %d МБ в распакованном виде", pluginUnpackMax>>20)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, m, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, int64(pluginUnpackMax-total+int(f.UncompressedSize64))+1))
		rc.Close()
		if err != nil {
			return nil, m, err
		}
		total += len(b) - int(f.UncompressedSize64) // заявленный размер мог соврать
		if total > pluginUnpackMax {
			return nil, m, fmt.Errorf("плагин больше %d МБ в распакованном виде", pluginUnpackMax>>20)
		}
		files[clean] = b
	}
	if _, ok := files["plugin.json"]; !ok { // всё в одной папке верхнего уровня — снимаем её
		prefix := ""
		for name := range files {
			if strings.HasSuffix(name, "/plugin.json") && strings.Count(name, "/") == 1 {
				prefix = strings.TrimSuffix(name, "plugin.json")
			}
		}
		if prefix == "" {
			return nil, m, errors.New("в архиве нет plugin.json")
		}
		inner := map[string][]byte{}
		for name, b := range files {
			if strings.HasPrefix(name, prefix) {
				inner[strings.TrimPrefix(name, prefix)] = b
			}
		}
		files = inner
	}
	if err := json.Unmarshal(files["plugin.json"], &m); err != nil {
		return nil, m, fmt.Errorf("plugin.json: %w", err)
	}
	if err := validManifest(m); err != nil {
		return nil, m, err
	}
	if m.Kind == "js" {
		if _, ok := files[m.Main]; !ok {
			return nil, m, fmt.Errorf("в архиве нет %s", m.Main)
		}
	}
	return files, m, nil
}

// InstallRepo — скачать последний релиз репозитория GitHub и поставить.
// Ищется файл jarvis-plugin-<arch>.zip, иначе jarvis-plugin.zip; для официальных — plugin-<id>[-arch].zip
// в релизе самого Джарвиса.
func (h *PluginHost) InstallRepo(ctx context.Context, repo, id string) (PluginManifest, error) {
	return h.installRepo(ctx, repo, id, "", false)
}

// installRepo — tag "" — последний релиз; forNext — см. install.
func (h *PluginHost) installRepo(ctx context.Context, repo, id, tag string, forNext bool) (PluginManifest, error) {
	var m PluginManifest
	if !repoRe.MatchString(repo) {
		return m, errors.New("репозиторий — вида owner/name")
	}
	official := strings.EqualFold(repo, defaultRepo)
	var names []string
	if official {
		if !pluginIDRe.MatchString(id) {
			return m, errors.New("не указан плагин")
		}
		names = []string{"plugin-" + id + "-" + pluginArch() + ".zip", "plugin-" + id + ".zip"}
	} else {
		names = []string{"jarvis-plugin-" + pluginArch() + ".zip", "jarvis-plugin.zip"}
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int    `json:"size"`
		} `json:"assets"`
	}
	relURL := githubAPI + "/repos/" + repo + "/releases/latest"
	if tag != "" {
		relURL = githubAPI + "/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	}
	if err := h.getJSON(ctx, relURL, &rel); err != nil {
		return m, fmt.Errorf("релиз %s: %w", repo, err)
	}
	link := ""
	for _, want := range names {
		for _, a := range rel.Assets {
			if a.Name == want && a.Size <= pluginZipMax {
				link = a.URL
				break
			}
		}
		if link != "" {
			break
		}
	}
	if link == "" {
		return m, fmt.Errorf("в последнем релизе %s нет %s", repo, strings.Join(names, " или "))
	}
	raw, err := h.download(ctx, link)
	if err != nil {
		return m, err
	}
	m, err = h.install(raw, repo, official, forNext)
	if err == nil && official && m.ID != id {
		log.Printf("плагины: ждали %s, а в архиве %s", id, m.ID)
	}
	return m, err
}

func (h *PluginHost) getJSON(ctx context.Context, link string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	req.Header.Set("User-Agent", "jarvisd/"+version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.github().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub ответил %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (h *PluginHost) download(ctx context.Context, link string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	req.Header.Set("User-Agent", "jarvisd/"+version)
	resp, err := h.github().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("скачивание: ответ %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, pluginZipMax+1))
	if err != nil {
		return nil, err
	}
	if len(b) > pluginZipMax {
		return nil, fmt.Errorf("архив плагина больше %d МБ", pluginZipMax>>20)
	}
	return b, nil
}

func (h *PluginHost) Uninstall(id string) error {
	h.mu.Lock()
	p := h.plugins[id]
	h.mu.Unlock()
	if p == nil {
		return errors.New("нет такого плагина")
	}
	h.stop(p)
	if err := os.RemoveAll(p.Dir); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.plugins, id)
	if st := h.state[id]; st != nil && st.Official {
		h.state[id] = &pluginState{Removed: true, Source: st.Source, Official: true, At: time.Now()}
	} else {
		delete(h.state, id)
	}
	h.saveLocked()
	h.mu.Unlock()
	log.Printf("плагины: удалён %s", id)
	return nil
}

func (h *PluginHost) SetEnabled(id string, on bool) error {
	h.mu.Lock()
	p := h.plugins[id]
	if p == nil {
		h.mu.Unlock()
		return errors.New("нет такого плагина")
	}
	st := h.stateLocked(id)
	was := st.Enabled
	st.Enabled = on
	h.saveLocked()
	h.mu.Unlock()
	if on && !was {
		h.start(p)
	}
	if !on && was {
		h.stop(p)
	}
	return nil
}

// SetSettings — сохранить настройки плагина. Пустой пароль — оставить прежний (панель не видит ключи целиком).
func (h *PluginHost) SetSettings(id string, in map[string]any) error {
	h.mu.Lock()
	p := h.plugins[id]
	if p == nil {
		h.mu.Unlock()
		return errors.New("нет такого плагина")
	}
	st := h.stateLocked(id)
	if st.Settings == nil {
		st.Settings = map[string]any{}
	}
	for _, s := range p.Manifest.Settings {
		v, ok := in[s.Key]
		if !ok {
			continue
		}
		if s.Type == "password" {
			if str, _ := v.(string); str == "" {
				continue
			}
		}
		if s.Type == "select" && len(s.Options) > 0 {
			if str, _ := v.(string); !slices.Contains(s.Options, str) {
				h.mu.Unlock()
				return fmt.Errorf("«%s»: нет такого варианта", s.Label)
			}
		}
		st.Settings[s.Key] = v
	}
	h.saveLocked()
	settings, rt := h.settingsLocked(p), p.rt
	h.mu.Unlock()
	if rt != nil {
		rt.Configure(settings)
	}
	return nil
}

// ---------------------------------------------------------------- для панели

type PluginInfo struct {
	PluginManifest
	Enabled  bool           `json:"enabled"`
	Running  bool           `json:"running"`
	Official bool           `json:"official"`
	Source   string         `json:"source"`
	Error    string         `json:"error,omitempty"`
	Status   string         `json:"status,omitempty"`
	Values   map[string]any `json:"values"`
	Size     int64          `json:"size"`
}

func (h *PluginHost) List() []PluginInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []PluginInfo{}
	for _, p := range h.plugins {
		st := h.stateLocked(p.Manifest.ID)
		vals := h.settingsLocked(p)
		for _, s := range p.Manifest.Settings { // ключи панели не отдаём — только признак, что задан
			if s.Type == "password" {
				if str, _ := vals[s.Key].(string); str != "" {
					vals[s.Key] = "•••" + tail(str, 4)
				}
			}
		}
		info := PluginInfo{PluginManifest: p.Manifest, Enabled: st.Enabled, Running: p.rt != nil, Official: st.Official,
			Source: st.Source, Error: p.err, Values: vals, Size: dirSize(p.Dir)}
		if p.rt != nil {
			info.Status = p.rt.Status()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func tail(s string, n int) string {
	if len(s) <= n*2 {
		return ""
	}
	return s[len(s)-n:]
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// CatalogItem — плагин в каталоге: официальный (из plugins/index.json) или найденный на GitHub.
type CatalogItem struct {
	ID          string      `json:"id"`
	Repo        string      `json:"repo"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Author      string      `json:"author,omitempty"`
	Kind        string      `json:"kind,omitempty"`
	Version     string      `json:"version,omitempty"`
	Icon        string      `json:"icon,omitempty"`
	Category    string      `json:"category"`
	Provides    []string    `json:"provides,omitempty"`
	Size        int         `json:"size_kb,omitempty"`
	Recommended bool        `json:"recommended,omitempty"`
	Auto        *PluginAuto `json:"auto,omitempty"`
	Official    bool        `json:"official"`
	Stars       int         `json:"stars,omitempty"`
	Updated     string      `json:"updated,omitempty"`
	URL         string      `json:"url,omitempty"`
}

// Catalog — официальные плагины и найденные на GitHub по теме yajarvis-plugin; кеш на час.
func (h *PluginHost) Catalog(ctx context.Context, fresh bool) ([]CatalogItem, string) {
	h.catMu.Lock()
	defer h.catMu.Unlock()
	if !fresh && h.cat != nil && time.Since(h.catAt) < time.Hour {
		return h.cat, h.catErr
	}
	var out []CatalogItem
	var errs []string
	var idx struct {
		Plugins []CatalogItem `json:"plugins"`
	}
	if err := h.getJSON(ctx, h.index, &idx); err != nil {
		errs = append(errs, "официальный список: "+err.Error())
	}
	for _, it := range idx.Plugins {
		if pluginIDRe.MatchString(it.ID) {
			it.Official, it.Repo = true, defaultRepo
			if !validCategory(it.Category) {
				it.Category = "other"
			}
			out = append(out, it)
		}
	}
	var found struct {
		Items []struct {
			FullName    string   `json:"full_name"`
			Name        string   `json:"name"`
			Description string   `json:"description"`
			HTMLURL     string   `json:"html_url"`
			Stars       int      `json:"stargazers_count"`
			Pushed      string   `json:"pushed_at"`
			Archived    bool     `json:"archived"`
			Topics      []string `json:"topics"`
			Owner       struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"items"`
	}
	q := url.QueryEscape("topic:" + pluginTopic)
	if err := h.getJSON(ctx, githubAPI+"/search/repositories?per_page=60&sort=stars&q="+q, &found); err != nil {
		errs = append(errs, "поиск на GitHub: "+err.Error())
	}
	for _, r := range found.Items {
		if r.Archived || strings.EqualFold(r.FullName, defaultRepo) {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(r.Name, "yajarvis-"), "jarvis-")
		out = append(out, CatalogItem{ID: strings.ToLower(name), Repo: r.FullName, Name: r.Name, Description: r.Description,
			Author: r.Owner.Login, Stars: r.Stars, Updated: r.Pushed, URL: r.HTMLURL, Category: topicCategory(r.Topics)})
	}
	h.cat, h.catAt, h.catErr = out, time.Now(), strings.Join(errs, "; ")
	if len(out) == 0 && h.catErr != "" { // не кешируем провал надолго
		h.catAt = time.Now().Add(-55 * time.Minute)
	}
	return out, h.catErr
}

// PluginAuto — когда официальный плагин ставится сам (index.json): чтобы обновление, которое переносит
// функцию из ядра в плагин, ничего не сломало. Always — всем; Settings — если в settings.json задано
// любое из этих полей (не пустое, не false, не 0).
type PluginAuto struct {
	Always   bool     `json:"always,omitempty"`
	Settings []string `json:"settings,omitempty"`
}

// needed — нужен ли плагин при таких настройках (settings — settings.json как карта).
func (a *PluginAuto) needed(settings map[string]any) bool {
	if a == nil {
		return false
	}
	if a.Always {
		return true
	}
	for _, k := range a.Settings {
		switch v := settings[k].(type) {
		case nil:
		case string:
			if v != "" {
				return true
			}
		case bool:
			if v {
				return true
			}
		case float64:
			if v != 0 {
				return true
			}
		case map[string]any:
			for _, value := range v {
				if (&PluginAuto{Settings: []string{"value"}}).needed(map[string]any{"value": value}) {
					return true
				}
			}
		case []any:
			if len(v) > 0 {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// SettingsMap — настройки колонки картой (для правил auto); задаётся в main.
var pluginSettingsMap func() map[string]any

// EnsureFor — поставить официальные плагины, которые нужны версии tag («v0.9.70») при текущих настройках.
// Вызывается перед обновлением ядра (не скачалось — обновление откладывается) и при каждом запуске
// (страховка, если ядро обновили вручную). Удалённые руками не ставит. Возвращает, что поставлено.
func (h *PluginHost) EnsureFor(ctx context.Context, tag string, forNext bool) ([]string, error) {
	ref := tag
	if ref == "" || ref == "vdev" {
		ref = "main"
	}
	var idx struct {
		Plugins []CatalogItem `json:"plugins"`
	}
	link := githubRaw + "/" + defaultRepo + "/" + url.PathEscape(ref) + "/plugins/index.json"
	if err := h.getJSON(ctx, link, &idx); err != nil {
		if strings.Contains(err.Error(), "404") { // у версии нет списка плагинов — ставить нечего
			return nil, nil
		}
		return nil, fmt.Errorf("список плагинов версии %s: %w", ref, err)
	}
	settings := map[string]any{}
	if pluginSettingsMap != nil {
		settings = pluginSettingsMap()
	}
	var done []string
	for _, it := range idx.Plugins {
		if !pluginIDRe.MatchString(it.ID) || !it.Auto.needed(settings) {
			continue
		}
		h.mu.Lock()
		_, have := h.plugins[it.ID]
		removed := h.state[it.ID] != nil && h.state[it.ID].Removed
		h.mu.Unlock()
		if have || removed {
			continue
		}
		relTag := tag
		if ref == "main" {
			relTag = ""
		}
		if _, err := h.installRepo(ctx, defaultRepo, it.ID, relTag, forNext); err != nil {
			return done, fmt.Errorf("плагин «%s»: %w", it.Name, err)
		}
		log.Printf("плагины: %s поставлен сам — он нужен Джарвису %s", it.ID, ref)
		done = append(done, it.ID)
	}
	return done, nil
}

// EnsureLoop — при запуске: поставить нужные этой версии плагины; нет интернета — пробовать снова.
func (h *PluginHost) EnsureLoop() {
	for wait := time.Minute; ; wait = min(wait*2, time.Hour) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_, err := h.EnsureFor(ctx, "v"+version, false)
		cancel()
		if err == nil {
			return
		}
		log.Printf("плагины: %v — попробую через %s", err, wait)
		time.Sleep(wait)
	}
}

// topicCategory — раздел стороннего плагина по темам репозитория: yajarvis-home → home; нет — «Другое».
func topicCategory(topics []string) string {
	for _, t := range topics {
		if c := strings.TrimPrefix(t, "yajarvis-"); c != t && validCategory(c) {
			return c
		}
	}
	return "other"
}

// pluginDataPath — где плагин хранит свои данные (переживают обновление плагина).
func pluginDataPath(p *Plugin) string { return filepath.Join(p.Dir, "data.json") }
