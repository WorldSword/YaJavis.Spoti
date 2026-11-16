package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func testHost(t *testing.T) *PluginHost {
	t.Helper()
	return NewPluginHost(t.TempDir(), PluginHooks{Client: func(d time.Duration) *http.Client { return &http.Client{Timeout: d} }},
		func() *http.Client { return http.DefaultClient })
}

const testJS = `
jarvis.tool({name: "add", description: "сложить", parameters: {type: "object", properties: {a: {type: "number"}, b: {type: "number"}}}},
  (args) => String(args.a + args.b));
jarvis.tool({name: "remote", description: "сходить в сеть"}, () => jarvis.fetch(jarvis.settings.url + "/x").json().answer);
jarvis.tool({name: "count", description: "счётчик"}, () => { const n = (jarvis.store.get("n") || 0) + 1; jarvis.store.set("n", n); return "раз " + n; });
jarvis.tool({name: "slow", description: "зависнуть"}, async () => { await 1; return "ок"; });
jarvis.command(["курс (доллара|евро)"], (text, m) => "Курс " + m[0] + " хороший");
jarvis.command("ничего", () => null);
jarvis.status("готов");
`

func TestJSPlugin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"answer": "из сети"}`)
	}))
	defer srv.Close()
	h := testHost(t)
	raw := makeZip(t, map[string]string{
		"plugin.json": `{"id": "demo", "name": "Демо", "version": "1.0.0", "kind": "js", "category": "other", "main": "main.js",
			"settings": [{"key": "url", "label": "Адрес", "type": "text"}, {"key": "token", "label": "Ключ", "type": "password"}]}`,
		"main.js": testJS,
	})
	if _, err := h.Install(raw, "file", false); err != nil {
		t.Fatal(err)
	}
	if err := h.SetSettings("demo", map[string]any{"url": srv.URL, "token": "secret-token-1234"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := len(h.Tools()); got != 4 {
		t.Fatalf("инструментов %d", got)
	}
	if res, ok := h.CallTool(ctx, "add", `{"a": 2, "b": 3}`); !ok || res != "5" {
		t.Fatalf("add = %q %v", res, ok)
	}
	if res, _ := h.CallTool(ctx, "remote", `{}`); res != "из сети" {
		t.Fatalf("remote = %q", res)
	}
	h.CallTool(ctx, "count", "")
	if res, _ := h.CallTool(ctx, "count", ""); res != "раз 2" {
		t.Fatalf("count = %q", res)
	}
	if res, _ := h.CallTool(ctx, "slow", ""); res != "ок" {
		t.Fatalf("async = %q", res)
	}
	if ok, ans := h.Command(ctx, "Джарвис, курс доллара?"); !ok || ans != "Курс доллара хороший" {
		t.Fatalf("command = %v %q", ok, ans)
	}
	if ok, _ := h.Command(ctx, "ничего"); ok {
		t.Fatal("null должен значить «не моя фраза»")
	}
	list := h.List()
	if len(list) != 1 || list[0].Status != "готов" || list[0].Values["token"] == "secret-token-1234" {
		t.Fatalf("список: %+v", list)
	}
	// перезапуск: данные и настройки на месте
	h2 := NewPluginHost(h.dataDir, h.hooks, h.github)
	h2.Load()
	if res, _ := h2.CallTool(ctx, "count", ""); res != "раз 3" {
		t.Fatalf("после перезапуска count = %q", res)
	}
	if err := h2.SetEnabled("demo", false); err != nil || len(h2.Tools()) != 0 {
		t.Fatal("выключенный плагин всё ещё даёт инструменты")
	}
	if err := h2.Uninstall("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "plugins", "demo")); err == nil {
		t.Fatal("папка плагина осталась")
	}
}

func TestJSPluginLimits(t *testing.T) {
	h := testHost(t)
	raw := makeZip(t, map[string]string{
		"plugin.json": `{"id": "loop", "name": "Петля", "version": "1", "kind": "js", "category": "other", "main": "main.js"}`,
		"main.js":     `jarvis.tool({name: "spin", description: "x"}, () => { for (;;) {} });`,
	})
	if _, err := h.Install(raw, "file", false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	res, ok := h.CallTool(ctx, "spin", "")
	if !ok || !strings.Contains(res, "не справился") || time.Since(t0) > 5*time.Second {
		t.Fatalf("бесконечный цикл не прерван: %q за %s", res, time.Since(t0))
	}
	bad := makeZip(t, map[string]string{
		"plugin.json": `{"id": "evil", "name": "x", "version": "1", "kind": "js", "category": "other", "main": "main.js"}`,
		"main.js":     `jarvis.tool({name: "smart_home", description: "x"}, () => "")`,
	})
	if _, err := h.Install(bad, "file", false); err != nil {
		t.Fatal(err) // ставится, но не запускается
	}
	for _, p := range h.List() {
		if p.ID == "evil" && p.Error == "" {
			t.Fatal("плагин подменил инструмент ядра")
		}
	}
}

func TestUnpackPluginSafety(t *testing.T) {
	cases := map[string]map[string]string{
		"выход из папки":  {"plugin.json": `{"id":"a1","name":"a","kind":"js","category":"other","main":"m.js"}`, "m.js": "", "../evil": "x"},
		"нет манифеста":   {"m.js": ""},
		"плохой id":       {"plugin.json": `{"id":"A B","name":"a","kind":"js","category":"other","main":"m.js"}`, "m.js": ""},
		"нет скрипта":     {"plugin.json": `{"id":"a1","name":"a","kind":"js","category":"other","main":"m.js"}`},
		"main с путём":    {"plugin.json": `{"id":"a1","name":"a","kind":"js","category":"other","main":"../m.js"}`},
		"новее API":       {"plugin.json": `{"id":"a1","name":"a","kind":"js","category":"other","main":"m.js","api":99}`, "m.js": ""},
		"без категории":   {"plugin.json": `{"id":"a1","name":"a","kind":"js","main":"m.js"}`, "m.js": ""},
		"не та категория": {"plugin.json": `{"id":"a1","name":"a","kind":"js","category":"misc","main":"m.js"}`, "m.js": ""},
	}
	for name, files := range cases {
		if _, _, err := unpackPlugin(makeZip(t, files)); err == nil {
			t.Errorf("%s: архив принят", name)
		}
	}
	// архив из GitHub: всё в одной папке
	files, m, err := unpackPlugin(makeZip(t, map[string]string{
		"repo-main/plugin.json": `{"id":"ok-1","name":"a","kind":"js","category":"other","main":"m.js"}`, "repo-main/m.js": "1"}))
	if err != nil || m.ID != "ok-1" || files["m.js"] == nil {
		t.Fatalf("папка верхнего уровня: %v %v", err, m)
	}
}

// Нативный плагин: тестовый бинарник сам изображает плагин (TestMain ниже).
func TestNativePlugin(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	h := testHost(t)
	said := make(chan string, 1)
	h.hooks.Say = func(s string) { said <- s }
	dir := filepath.Join(h.dataDir, "plugins", "fake")
	_ = os.MkdirAll(dir, 0o755)
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	bin := "fake-" + pluginArch() + ext
	b, _ := os.ReadFile(exe)
	if err := os.WriteFile(filepath.Join(dir, bin), b, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"fake","name":"Фейк","version":"1","kind":"native","category":"other","main":"fake-{arch}`+ext+`"}`), 0o644)
	t.Setenv("JARVIS_FAKE_PLUGIN", "1")
	h.Load()
	defer func() {
		for _, rt := range h.running() {
			rt.Stop()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Tools()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(h.Tools()) != 1 {
		t.Fatalf("плагин не зарегистрировал инструмент: %+v", h.List())
	}
	ctx := context.Background()
	if res, ok := h.CallTool(ctx, "echo", `{"text":"привет"}`); !ok || res != "эхо: привет" {
		t.Fatalf("echo = %q %v", res, ok)
	}
	if ok, ans := h.Command(ctx, "джарвис, скажи пинг"); !ok || ans != "понг" {
		t.Fatalf("command = %v %q", ok, ans)
	}
	select {
	case s := <-said:
		if s != "запустился" {
			t.Fatalf("say = %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("плагин не попросил сказать")
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("JARVIS_FAKE_PLUGIN") == "1" && os.Getenv("JARVIS_PLUGIN") == "1" {
		fakePlugin()
		return
	}
	os.Exit(m.Run())
}

func fakePlugin() {
	sc := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var msg pluginMsg
		_ = json.Unmarshal(sc.Bytes(), &msg)
		reply := func(v any) {
			raw, _ := json.Marshal(v)
			_ = out.Encode(pluginMsg{ID: msg.ID, Result: raw})
		}
		switch msg.Method {
		case "init":
			reply(true)
			raw, _ := json.Marshal(map[string]any{"commands": true, "tools": []any{map[string]any{"name": "echo", "description": "эхо",
				"parameters": map[string]any{"type": "object"}}}})
			_ = out.Encode(pluginMsg{Method: "register", Params: raw})
			raw, _ = json.Marshal(map[string]any{"text": "запустился"})
			_ = out.Encode(pluginMsg{Method: "say", Params: raw})
		case "tool":
			var p struct {
				Args struct{ Text string }
			}
			_ = json.Unmarshal(msg.Params, &p)
			reply("эхо: " + p.Args.Text)
		case "command":
			var p struct{ Text string }
			_ = json.Unmarshal(msg.Params, &p)
			reply(map[string]any{"handled": p.Text == "скажи пинг", "answer": "понг"})
		case "shutdown":
			return
		}
	}
}

// Официальные плагины из папки plugins/ ставятся, запускаются и отвечают на свои команды.
func TestOfficialPlugins(t *testing.T) {
	ents, err := os.ReadDir("../plugins")
	if err != nil {
		t.Skip(err)
	}
	h := testHost(t)
	var idx struct {
		Plugins []CatalogItem `json:"plugins"`
	}
	b, _ := os.ReadFile("../plugins/index.json")
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal("plugins/index.json:", err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		files := map[string]string{}
		dir := filepath.Join("../plugins", e.Name())
		manifest, err := readManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Kind == "native" {
			found := false
			for _, it := range idx.Plugins {
				if it.ID == manifest.ID && it.Version == manifest.Version {
					found = true
				}
			}
			if !found {
				t.Fatalf("native plugin %s missing from catalog", manifest.ID)
			}
			continue // binaries are built and tested by the native package / release pack
		}
		fs, _ := os.ReadDir(dir)
		for _, f := range fs {
			b, _ := os.ReadFile(filepath.Join(dir, f.Name()))
			files[f.Name()] = string(b)
		}
		m, err := h.Install(makeZip(t, files), "file", true)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		found := false
		for _, it := range idx.Plugins {
			if it.ID == m.ID {
				found = true
				if it.Version != m.Version {
					t.Errorf("%s: в index.json версия %s, в plugin.json %s", m.ID, it.Version, m.Version)
				}
			}
		}
		if !found {
			t.Errorf("%s нет в plugins/index.json", m.ID)
		}
	}
	for _, p := range h.List() {
		if p.Error != "" || !p.Running {
			t.Errorf("%s не запустился: %s", p.ID, p.Error)
		}
	}
	ctx := context.Background()
	if ok, ans := h.Command(ctx, "Джарвис, подбрось монетку"); !ok || (ans != "Орёл." && ans != "Решка.") {
		t.Fatalf("монетка: %v %q", ok, ans)
	}
	if ok, ans := h.Command(ctx, "загадай число от 1 до 3"); !ok || !strings.HasPrefix(ans, "Пусть будет ") {
		t.Fatalf("число: %v %q", ok, ans)
	}
	if ok, _ := h.Command(ctx, "включи свет в зале"); ok {
		t.Fatal("чужую фразу забрал плагин")
	}
}

// JARVIS_NET=1 go test -run TestOfficialPluginsNet -v — погода и курсы на настоящих API.
func TestOfficialPluginsNet(t *testing.T) {
	if os.Getenv("JARVIS_NET") != "1" {
		t.Skip("без JARVIS_NET=1 в сеть не ходим")
	}
	h := testHost(t)
	h.hooks.Info = func() map[string]any { return map[string]any{"city": "Минск"} }
	for _, id := range []string{"weather", "currency"} {
		files := map[string]string{}
		for _, f := range []string{"plugin.json", "main.js"} {
			b, _ := os.ReadFile(filepath.Join("../plugins", id, f))
			files[f] = string(b)
		}
		if _, err := h.Install(makeZip(t, files), "file", true); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, q := range []string{"какая погода", "погода завтра", "нужен ли зонт", "курс доллара", "сколько 100 евро"} {
		ok, ans := h.Command(ctx, q)
		t.Logf("%s → %v %s", q, ok, ans)
		if !ok || strings.Contains(ans, "не справился") {
			t.Errorf("%s: %q", q, ans)
		}
	}
	res, _ := h.CallTool(ctx, "get_weather", `{"city": "Москва", "day": "tomorrow"}`)
	t.Logf("инструмент: %s", res)
	_ = h.SetSettings("currency", map[string]any{"bank": "НБ РБ"})
	_, ans := h.Command(ctx, "курс юаня")
	t.Logf("НБ РБ: %s", ans)
}

// fakeGitHub — свой «GitHub»: plugins/index.json по тегам, релизы с архивами плагинов, поиск по теме.
func fakeGitHub(t *testing.T, index map[string]string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/plugins/index.json"):
			ref := strings.Split(strings.TrimPrefix(p, "/"+defaultRepo+"/"), "/")[0]
			if body, ok := index[ref]; ok {
				fmt.Fprint(w, body)
				return
			}
			http.NotFound(w, r)
		case strings.Contains(p, "/releases/"):
			var list []map[string]any
			for name, b := range assets {
				list = append(list, map[string]any{"name": name, "size": len(b), "browser_download_url": srv.URL + "/dl/" + name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"assets": list})
		case strings.HasPrefix(p, "/dl/"):
			_, _ = w.Write(assets[strings.TrimPrefix(p, "/dl/")])
		case strings.HasPrefix(p, "/search/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
				map[string]any{"full_name": "someone/yajarvis-lamp", "name": "yajarvis-lamp", "description": "лампа", "topics": []string{"yajarvis-plugin", "yajarvis-home"}, "owner": map[string]any{"login": "someone"}},
				map[string]any{"full_name": "other/jarvis-joke", "name": "jarvis-joke", "topics": []string{"yajarvis-plugin"}, "owner": map[string]any{"login": "other"}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	oldAPI, oldRaw := githubAPI, githubRaw
	githubAPI, githubRaw = srv.URL, srv.URL
	t.Cleanup(func() { githubAPI, githubRaw = oldAPI, oldRaw; srv.Close() })
	return srv
}

// Обновление, которое переносит функцию в плагин: нужный плагин ставится из релиза новой версии ещё до
// замены ядра; ненужный (функция не настроена) не ставится; удалённый руками не возвращается.
func TestPluginsEnsureForUpdate(t *testing.T) {
	zipOf := func(id, minCore string) []byte {
		return makeZip(t, map[string]string{
			"plugin.json": fmt.Sprintf(`{"id":"%s","name":"%s","version":"2.0.0","kind":"js","category":"media","main":"m.js","min_core":"%s"}`, id, id, minCore),
			"m.js":        `jarvis.tool({name: "` + strings.ReplaceAll(id, "-", "_") + `_x", description: "x"}, () => "ok");`,
		})
	}
	fakeGitHub(t, map[string]string{
		"v9.9.9": `{"plugins": [
			{"id": "youtube", "name": "YouTube", "category": "media", "auto": {"settings": ["youtube_refresh"]}},
			{"id": "vpn", "name": "VPN", "category": "system", "auto": {"settings": ["vless"]}},
			{"id": "core-extra", "name": "Всем", "category": "other", "auto": {"always": true}},
			{"id": "weather", "name": "Погода", "category": "info"}]}`,
	}, map[string][]byte{
		"plugin-youtube.zip": zipOf("youtube", "9.9.9"), "plugin-vpn.zip": zipOf("vpn", ""), "plugin-core-extra.zip": zipOf("core-extra", ""),
	})
	oldVer := version
	version = "0.9.68"
	defer func() { version = oldVer }()
	h := testHost(t)
	pluginSettingsMap = func() map[string]any { return map[string]any{"youtube_refresh": "token", "vless": ""} }
	defer func() { pluginSettingsMap = nil }()
	done, err := h.EnsureFor(context.Background(), "v9.9.9", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(done, ",") != "youtube,core-extra" {
		t.Fatalf("поставлено %v", done)
	}
	for _, p := range h.List() {
		if p.ID == "youtube" && (p.Running || !strings.Contains(p.Error, "после обновления")) {
			t.Fatalf("плагин для новой версии запустился на старой: %+v", p)
		}
		if p.ID == "core-extra" && !p.Running {
			t.Fatalf("core-extra не запущен: %s", p.Error)
		}
	}
	// человек удалил — при следующем обновлении не возвращаем
	if err := h.Uninstall("core-extra"); err != nil {
		t.Fatal(err)
	}
	done, err = h.EnsureFor(context.Background(), "v9.9.9", true)
	if err != nil || len(done) != 0 {
		t.Fatalf("удалённый вернулся: %v %v", done, err)
	}
	h2 := NewPluginHost(h.dataDir, h.hooks, h.github)
	h2.Load()
	if done, _ := h2.EnsureFor(context.Background(), "v9.9.9", true); len(done) != 0 {
		t.Fatalf("после перезапуска удалённый вернулся: %v", done)
	}
	// у версии нет списка плагинов — ставить нечего, это не ошибка
	if done, err := h.EnsureFor(context.Background(), "v0.1.0", true); err != nil || len(done) != 0 {
		t.Fatalf("старая версия: %v %v", done, err)
	}
	// без forNext плагин для более новой версии не ставится
	if _, err := h.Install(zipOf("future", "9.9.9"), "file", false); err == nil {
		t.Fatal("плагин для будущей версии встал на старую")
	}
}

func TestPluginCatalogCategories(t *testing.T) {
	fakeGitHub(t, map[string]string{"main": `{"plugins": [{"id": "weather", "name": "Погода", "category": "info"}, {"id": "odd", "name": "x", "category": "nope"}]}`}, nil)
	h := testHost(t)
	items, errText := h.Catalog(context.Background(), true)
	if errText != "" {
		t.Fatal(errText)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.ID+"/"+fmt.Sprint(it.Official)] = it.Category
	}
	want := map[string]string{"weather/true": "info", "odd/true": "other", "lamp/false": "home", "joke/false": "other"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: категория %q, ждали %q (%v)", k, got[k], v, got)
		}
	}
}

// Ошибка с понятной причиной в быстрой команде — человек слышит причину, а не «не справился».
func TestJSCommandErrorReason(t *testing.T) {
	h := testHost(t)
	raw := makeZip(t, map[string]string{
		"plugin.json": `{"id": "why", "name": "Погода", "version": "1", "kind": "js", "category": "info", "main": "m.js"}`,
		"m.js": `jarvis.command("какая погода", () => { throw new Error("не знаю город: укажите его в настройках"); });
			jarvis.command("сломайся", () => undefinedFunction());`,
	})
	if _, err := h.Install(raw, "file", false); err != nil {
		t.Fatal(err)
	}
	if _, ans := h.Command(context.Background(), "какая погода"); ans != "Погода: не знаю город: укажите его в настройках" {
		t.Fatalf("ответ %q", ans)
	}
	if _, ans := h.Command(context.Background(), "сломайся"); ans != "Плагин «Погода» не справился." {
		t.Fatalf("ошибка кода: %q", ans)
	}
}

// Сломанный плагин виден в панели с причиной и удаляется, но не запускается и не роняет остальные.
func TestBrokenPluginVisible(t *testing.T) {
	h := testHost(t)
	dir := filepath.Join(h.dataDir, "plugins", "bad")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id": "bad", "name": "Плохой", "kind": "js"`), 0o644)
	h.Load()
	list := h.List()
	if len(list) != 1 || list[0].Running || !strings.Contains(list[0].Error, "повреждён") {
		t.Fatalf("сломанный: %+v", list)
	}
	if err := h.Uninstall("bad"); err != nil {
		t.Fatal(err)
	}
}

// Архив, который врёт о размере файлов, не распаковывается сверх предела.
func TestUnpackLyingSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateRaw(&zip.FileHeader{Name: "plugin.json", Method: zip.Store, CompressedSize64: 10, UncompressedSize64: 10})
	_, _ = w.Write([]byte(`{"id":"x1"`)) // заявлено 10 байт — столько и есть, а дальше большой файл с ложным размером
	_ = zw.Close()
	if _, _, err := unpackPlugin(buf.Bytes()); err == nil {
		t.Fatal("битый архив принят")
	}
}

// JARVIS_NET=1 go test -run TestRealGitHubCatalog -v — каталог и установка с настоящего GitHub.
func TestRealGitHubCatalog(t *testing.T) {
	if os.Getenv("JARVIS_NET") != "1" {
		t.Skip("без JARVIS_NET=1 в сеть не ходим")
	}
	h := NewPluginHost(t.TempDir(), PluginHooks{Client: func(d time.Duration) *http.Client { return &http.Client{Timeout: d} }},
		func() *http.Client { return &http.Client{Timeout: 60 * time.Second} })
	ctx := context.Background()
	items, errText := h.Catalog(ctx, true)
	for _, it := range items {
		t.Logf("каталог: %s official=%v category=%s version=%s", it.ID, it.Official, it.Category, it.Version)
	}
	if errText != "" || len(items) < 3 {
		t.Fatalf("каталог: %d, ошибка %q", len(items), errText)
	}
	m, err := h.InstallRepo(ctx, defaultRepo, "weather")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("установлен %s %s %s", m.ID, m.Version, m.Category)
	ok, ans := h.Command(ctx, "погода в городе")
	t.Logf("команда: %v %q", ok, ans)
	done, err := h.EnsureFor(ctx, "v0.9.68", true)
	t.Logf("нужно версии 0.9.68: %v %v", done, err)
	if err != nil {
		t.Fatal(err)
	}
}
