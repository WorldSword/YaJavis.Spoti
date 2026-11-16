// Установка Джарвиса на Яндекс Станцию. Запускаете — открывается страница в браузере:
// программа сама находит колонку в сети, проверяет её и ставит/обновляет/удаляет Джарвиса по ADB.
// Нужна уже разлоченная колонка с ADB по сети (порт 5555) и правами root.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/newfpv/YaJavis/hardware"

	"golang.org/x/crypto/bcrypt"
)

//go:embed ui payload
var files embed.FS

var version = "dev"

const remoteDir = "/data/opt/jarvisd"

type Speaker struct {
	ModelID   string   `json:"model_id"`
	IP        string   `json:"ip"`
	Model     string   `json:"model"`
	Arch      string   `json:"arch"`     // arm64 | armv7
	Userland  string   `json:"userland"` // armhf | arm64 — для vqe_tap.so (как у maind)
	Root      bool     `json:"root"`
	Android   bool     `json:"android"`
	Installed string   `json:"installed"` // версия jarvisd или ""
	FreeMB    int      `json:"free_mb"`
	RAMMB     int      `json:"ram_mb"`
	OldJarvis bool     `json:"old_jarvis"` // серверный Джарвис (jarvis.sh)
	OldAlisa  bool     `json:"old_alisa"`  // Alisa Voice (rison) с собственным vqe_tap
	NeedMB    int      `json:"need_mb"`    // сколько места нужно под Джарвиса
	JunkKB    int      `json:"junk_kb"`    // кэш и старые копии Джарвиса — можно стереть
	Addons    []Addon  `json:"addons"`     // моды сообщества, найденные на колонке
	AutoFree  []string `json:"auto_free"`  // что удалим сами, если места не хватит (id модов)
	Problems  []string `json:"problems"`
	Notes     []string `json:"notes"`
}

type job struct {
	mu    sync.Mutex
	lines []string
	done  bool
	err   string
	url   string
}

func (j *job) log(format string, a ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, fmt.Sprintf(format, a...))
}

var (
	jobMu sync.Mutex
	cur   *job
)

func main() {
	addr := "127.0.0.1:0"
	if v := os.Getenv("JARVIS_SETUP_ADDR"); v != "" { // для проверки: фиксированный порт
		addr = v
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	ui, _ := fs.Sub(files, "ui")
	token := randToken()
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	api := func(path string, h http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			// страница открыта только этой программой: чужие сайты не могут дёргать установщик
			if r.Header.Get("X-Token") != token {
				http.Error(w, "forbidden", 403)
				return
			}
			h(w, r)
		})
	}
	mux.HandleFunc("GET /token.js", func(w http.ResponseWriter, r *http.Request) {
		if host, _, _ := net.SplitHostPort(r.Host); host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprintf(w, "window.TOKEN=%q;window.VERSION=%q;", token, version)
	})
	api("GET /api/scan", handleScan)
	api("POST /api/check", handleCheck)
	api("POST /api/install", handleInstall)
	api("POST /api/uninstall", handleUninstall)
	api("GET /api/job", handleJob)
	url := "http://" + ln.Addr().String() + "/"
	fmt.Println("Установка Джарвиса:", url)
	fmt.Println("Окно браузера откроется само. Не закрывайте это окно, пока идёт установка.")
	if os.Getenv("JARVIS_SETUP_NOBROWSER") == "" {
		openBrowser(url)
	}
	log.Fatal(http.Serve(ln, mux))
}

func randToken() string {
	b := make([]byte, 16)
	_, _ = cryptoRand(b)
	return fmt.Sprintf("%x", b)
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------- поиск колонок

func localNets() []*net.IPNet {
	var out []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
				continue
			}
			ones, _ := n.Mask.Size()
			if ones < 22 { // большие сети целиком не сканируем — берём /24 вокруг себя
				n = &net.IPNet{IP: n.IP.Mask(net.CIDRMask(24, 32)), Mask: net.CIDRMask(24, 32)}
			}
			out = append(out, &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask})
		}
	}
	return out
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	var mu sync.Mutex
	var found []string
	var wg sync.WaitGroup
	sem := make(chan struct{}, 128)
	for _, n := range localNets() {
		ip := n.IP.To4()
		ones, _ := n.Mask.Size()
		count := 1 << (32 - ones)
		base := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
		for i := 1; i < count-1; i++ {
			v := base + uint32(i)
			addr := fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&255, v>>8&255, v&255)
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				c, err := net.DialTimeout("tcp", addr+":5555", 600*time.Millisecond)
				if err != nil {
					return
				}
				c.Close()
				mu.Lock()
				found = append(found, addr)
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	sort.Strings(found)
	var res []Speaker
	for _, ip := range found {
		sp, err := inspect(ip)
		if err != nil {
			res = append(res, Speaker{IP: ip, Problems: []string{err.Error()}})
			continue
		}
		res = append(res, *sp)
	}
	writeJSON(w, 200, map[string]any{"speakers": res})
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	var req struct{ IP string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	ip := strings.TrimSpace(req.IP)
	if net.ParseIP(ip) == nil {
		writeJSON(w, 400, map[string]any{"error": "Это не IP-адрес"})
		return
	}
	sp, err := inspect(ip)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, sp)
}

// inspect — что за колонка и можно ли на неё ставить.
func inspect(ip string) (*Speaker, error) {
	a, err := DialADB(ip+":5555", 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("нет ADB на %s: %v", ip, err)
	}
	defer a.Close()
	sh := func(cmd string) string {
		out, _, _ := a.Shell(cmd, 15*time.Second)
		return strings.TrimSpace(out)
	}
	sp := &Speaker{IP: ip}
	sp.Root = sh("id -u") == "0"
	sp.Android = sh("getprop ro.build.version.sdk 2>/dev/null") != ""
	sp.Model = strings.Trim(sh("cat /proc/device-tree/model 2>/dev/null"), "\x00 ")
	if sp.Model == "" {
		sp.Model = sh("getprop ro.product.model 2>/dev/null")
	}
	board := hardware.Detect(sh("cat /proc/device-tree/compatible 2>/dev/null"), sh("cat /proc/device-tree/amlogic-dt-id 2>/dev/null"), sh("getprop ro.product.device 2>/dev/null"), sp.Model)
	sp.ModelID, sp.Model = board.ID, board.Name
	switch m := sh("uname -m"); {
	case strings.Contains(m, "aarch64") || strings.Contains(m, "arm64"):
		sp.Arch = "arm64"
	case strings.HasPrefix(m, "arm"):
		sp.Arch = "armv7"
	default:
		sp.Problems = append(sp.Problems, "Неизвестный процессор: "+m)
	}
	// разрядность программ Алисы (vqe_tap.so должен совпадать с maind)
	sp.Userland = elfUserland(sh("od -An -tu1 -N20 /system/vendor/quasar/maind 2>/dev/null"))
	sp.Installed = sh(remoteDir + "/jarvisd version 2>/dev/null")
	if kb, err := strconv.Atoi(sh("df -k /data 2>/dev/null | awk 'NR==2{print $4}'")); err == nil {
		sp.FreeMB = kb / 1024
	}
	if kb, err := strconv.Atoi(sh("awk '/MemTotal/{print $2}' /proc/meminfo")); err == nil {
		sp.RAMMB = kb / 1024
	}
	sp.OldJarvis = sh("[ -f /data/opt/jarvis/enabled ] && echo y") == "y"
	sp.OldAlisa = sh("[ -f /etc/init.d/S99zz-alisa-voice ] && [ -d /data/opt/alisa-standalone ] && echo y") == "y"

	if sp.Android {
		sp.Problems = append(sp.Problems, "Это Станция на Android (Макс или Станция 1-го поколения) — она пока не поддерживается.")
	}
	if !sp.Root {
		sp.Problems = append(sp.Problems, "ADB без прав root. Нужна разлоченная колонка — см. hackalice.me.")
	}
	if sh("[ -x /system/vendor/quasar/maind ] && echo y") != "y" && !sp.Android {
		sp.Problems = append(sp.Problems, "Не найдена штатная программа Алисы (maind) — это точно Яндекс Станция?")
	}
	if sp.Userland == "" && !sp.Android {
		sp.Problems = append(sp.Problems, "Не удалось определить ARM ABI штатной Алисы — библиотеку микрофона безопасно выбрать нельзя.")
	}
	if !sp.Android && !supportsLaunch(sh("cat /system/vendor/quasar/launch.json 2>/dev/null")) {
		sp.Problems = append(sp.Problems, "Не найден совместимый профиль запуска maind --platformd для подключения микрофона.")
	}
	if !sp.Android && sh("[ -d /etc/init.d ] && command -v start-stop-daemon >/dev/null && command -v aplay >/dev/null && echo y") != "y" {
		sp.Problems = append(sp.Problems, "Прошивке нужны init.d, start-stop-daemon и aplay; этот вариант ОС пока не поддерживается.")
	}
	if board.ID == "hub" {
		sp.Problems = append(sp.Problems, "Умный хаб не является голосовой колонкой и не поддерживается.")
	}
	if board.ID != "lite" {
		sp.Notes = append(sp.Notes, "Модель определена автоматически. На этой модели полный голосовой тракт YaJavis ещё не проверен; подсветка и микрофон зависят от прошивки.")
	}
	sp.Addons = probeAddons(sh)
	sp.JunkKB = probeJunkKB(sh)
	sp.NeedMB = needMB(sp, sh)
	if sp.FreeMB > 0 && sp.FreeMB < sp.NeedMB {
		total := sp.FreeMB*1024 + sp.JunkKB
		for _, a := range sp.Addons {
			total += a.KB
		}
		if total/1024 < sp.NeedMB {
			sp.Problems = append(sp.Problems, fmt.Sprintf("Мало места: свободно %d МБ, нужно около %d МБ, и даже чистая установка столько не освободит. Удалите лишнее с колонки вручную.", sp.FreeMB, sp.NeedMB))
		} else {
			_, rm := planCleanup(sp, false)
			for _, a := range rm {
				sp.AutoFree = append(sp.AutoFree, a.ID)
			}
		}
	}
	if sp.RAMMB > 0 && sp.RAMMB < 200 {
		sp.Notes = append(sp.Notes, fmt.Sprintf("Мало памяти (%d МБ) — Джарвис будет работать, но без запаса.", sp.RAMMB))
	}
	return sp, nil
}

// payloadFiles — что ляжет в /data/opt/jarvisd для этой колонки.
func payloadFiles(sp *Speaker) []string {
	names := []string{"jarvisd-" + sp.Arch, "vqe_tap-" + sp.Userland + ".so", "jarvisd.sh", "wake.bin", "oww.bin", "wake_oww.bin"}
	if sp.Userland != "arm64" {
		names = append(names, "spotcmd")
	}
	return names
}

// needMB — сколько места нужно на /data: файлы Джарвиса плюс запас на настройки и кэш голосов.
// При обновлении старые файлы заменяются (новая программа сначала ложится в /tmp), считаем только прирост.
func needMB(sp *Speaker, sh func(string) string) int {
	kb := 0
	for _, n := range payloadFiles(sp) {
		if b, err := files.ReadFile("payload/" + n); err == nil {
			kb += len(b) / 1024
		}
	}
	if sp.Installed == "" {
		return kb/1024 + 6
	}
	old := 0
	for _, line := range strings.Split(sh("cd "+remoteDir+" && du -sk jarvisd vqe_tap.so jarvisd.sh wake.bin oww.bin wake_oww.bin spotcmd 2>/dev/null"), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			n, _ := strconv.Atoi(f[0])
			old += n
		}
	}
	grow := kb - old
	if grow < 0 {
		grow = 0
	}
	return grow/1024 + 2
}

// ---------------------------------------------------------------- установка

func handleJob(w http.ResponseWriter, r *http.Request) {
	jobMu.Lock()
	j := cur
	jobMu.Unlock()
	if j == nil {
		writeJSON(w, 200, map[string]any{"lines": []string{}, "done": true})
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	writeJSON(w, 200, map[string]any{"lines": j.lines, "done": j.done, "error": j.err, "url": j.url})
}

func startJob(fn func(j *job) error) error {
	jobMu.Lock()
	defer jobMu.Unlock()
	if cur != nil && !cur.done {
		return errors.New("уже идёт установка")
	}
	j := &job{}
	cur = j
	go func() {
		err := fn(j)
		j.mu.Lock()
		j.done = true
		if err != nil {
			j.err = err.Error()
			j.lines = append(j.lines, "Ошибка: "+err.Error())
		}
		j.mu.Unlock()
	}()
	return nil
}

func payload(name string) []byte {
	b, err := files.ReadFile("payload/" + name)
	if err != nil {
		panic("в установщике нет " + name)
	}
	return b
}

func handleInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP, Password, OpenAI, Vless, Timezone string
		Clean                                 bool
		Soloist                               SoloistOpts // необязательно: ключ Soloist — тогда ставим и Spotify
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "пустой запрос"})
		return
	}
	if req.Password != "" && len([]rune(req.Password)) < 6 {
		writeJSON(w, 400, map[string]any{"error": "Пароль — минимум 6 символов"})
		return
	}
	if err := req.Soloist.validate(); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	err := startJob(func(j *job) error {
		return install(j, req.IP, req.Password, req.OpenAI, req.Vless, req.Timezone, req.Clean, req.Soloist)
	})
	if err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func install(j *job, ip, password, openai, vless, tz string, clean bool, spotify SoloistOpts) error {
	j.log("Подключаюсь к колонке %s…", ip)
	sp, err := inspect(ip)
	if err != nil {
		return err
	}
	if len(sp.Problems) > 0 {
		return errors.New(strings.Join(sp.Problems, " "))
	}
	j.log("Определена %s; программа %s, микрофон %s.", sp.Model, sp.Arch, sp.Userland)
	a, err := DialADB(ip+":5555", 5*time.Second)
	if err != nil {
		return err
	}
	defer a.Close()
	run := func(cmd string) (string, error) {
		out, rc, err := a.Shell(cmd, 60*time.Second)
		if err != nil {
			return out, err
		}
		if rc != 0 {
			return out, fmt.Errorf("команда на колонке не прошла (%d): %s", rc, strings.TrimSpace(out))
		}
		return out, nil
	}
	upgrade := sp.Installed != ""
	if upgrade {
		j.log("Найден Джарвис %s — обновляю, настройки сохраняются.", sp.Installed)
		_, _ = run(remoteDir + "/jarvisd.sh stop 2>/dev/null; true")
	}
	// место: при чистой установке убираем все найденные моды, иначе — только сколько нужно
	junk, remove := planCleanup(sp, clean)
	if junk {
		j.log("Стираю кэш голосов и старые копии Джарвиса (%d МБ)…", (sp.JunkKB+512)/1024)
		_, _ = run("rm -rf " + junkPaths + "; true")
	}
	for _, ad := range remove {
		j.log("Удаляю %s — %s (%d МБ)…", ad.Name, ad.About, (ad.KB+512)/1024)
		if _, _, err := a.Shell(removeScript(ad), 90*time.Second); err != nil {
			return fmt.Errorf("не удалось удалить %s: %v", ad.Name, err)
		}
		if ad.ID == "jarvis-server" {
			sp.OldJarvis = false
		}
		if ad.ID == "alisa-voice" {
			sp.OldAlisa = false
		}
	}
	if len(remove) > 0 || junk {
		if kb, err := strconv.Atoi(strings.TrimSpace(func() string { o, _, _ := a.Shell("df -k /data | awk 'NR==2{print $4}'", 10*time.Second); return o }())); err == nil {
			sp.FreeMB = kb / 1024
			j.log("Свободно на колонке: %d МБ.", sp.FreeMB)
		}
	}
	if sp.FreeMB > 0 && sp.FreeMB < sp.NeedMB {
		return fmt.Errorf("на колонке свободно %d МБ, а нужно около %d МБ — включите «Чистую установку» или освободите место", sp.FreeMB, sp.NeedMB)
	}
	if _, err := run("mkdir -p " + remoteDir); err != nil {
		return err
	}
	bin := "jarvisd-" + sp.Arch
	tap := "vqe_tap-" + sp.Userland + ".so"
	push := func(name, remote string, mode uint32) error {
		data := payload(name)
		last := -1
		j.log("Копирую %s (%d КБ)…", name, len(data)/1024)
		return a.Push(data, remote, mode, func(done, total int) {
			if p := done * 100 / total; p/25 != last/25 && total > 1<<20 {
				last = p
				j.log("  %d%%", p)
			}
		})
	}
	stage := remoteDir + "/jarvisd.new"
	if upgrade {
		stage = "/tmp/jarvisd.install"
	}
	if err := push(bin, stage, 0o755); err != nil {
		return err
	}
	if err := push(tap, remoteDir+"/vqe_tap.so", 0o644); err != nil {
		return err
	}
	if err := push("jarvisd.sh", remoteDir+"/jarvisd.sh", 0o755); err != nil {
		return err
	}
	if sp.Userland != "arm64" { // распознаватель команд Станции — 32-битный, как maind; без него Джарвис просто без мгновенных команд
		_ = push("spotcmd", remoteDir+"/spotcmd", 0o755)
	}
	if wake, err := files.ReadFile("payload/wake.bin"); err == nil {
		j.log("Копирую модель слова «Джарвис»…")
		if err := a.Push(wake, remoteDir+"/wake.bin", 0o644, nil); err != nil {
			return err
		}
	}
	// новое слово на признаках openWakeWord: если обе части есть, jarvisd берёт его вместо wake.bin
	for _, name := range []string{"oww.bin", "wake_oww.bin"} {
		if data, err := files.ReadFile("payload/" + name); err == nil {
			if err := a.Push(data, remoteDir+"/"+name, 0o644, nil); err != nil {
				return err
			}
		}
	}
	if _, err := run(stage + " version"); err != nil {
		return fmt.Errorf("новая программа не запускается на этой колонке: %v", err)
	}
	replace := "mv -f " + stage + " " + remoteDir + "/jarvisd"
	if upgrade {
		replace = "cp " + stage + " " + remoteDir + "/jarvisd && rm -f " + stage
	}
	if _, err := run(replace + " && chmod 755 " + remoteDir + "/jarvisd && " + remoteDir + "/jarvisd version"); err != nil {
		return fmt.Errorf("Джарвис не запускается на этой колонке: %v", err)
	}
	// начальные настройки (пароль сразу хэшем — открытым он на колонку не попадает)
	seed := map[string]string{}
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), 10)
		if err != nil {
			return err
		}
		seed["password_hash"] = string(h)
	}
	if strings.TrimSpace(openai) != "" {
		seed["openai_key"] = strings.TrimSpace(openai)
	}
	if strings.TrimSpace(vless) != "" {
		seed["vless"] = strings.TrimSpace(vless)
	}
	if tz != "" {
		seed["timezone"] = tz
	}
	if len(seed) > 0 {
		b, _ := json.Marshal(seed)
		if err := a.Push(b, remoteDir+"/seed.json", 0o600, nil); err != nil {
			return err
		}
	}
	if sp.OldJarvis {
		j.log("Выключаю серверного Джарвиса (файлы остаются в /data/opt/jarvis)…")
		_, _ = run("rm -f /data/opt/jarvis/enabled; /data/opt/jarvis/jarvis.sh stop >/dev/null 2>&1; true")
	}
	if sp.OldAlisa {
		j.log("Выключаю автозапуск Alisa Voice…")
		_, _ = run("mount -o remount,rw / && mv -f /etc/init.d/S99zz-alisa-voice /data/opt/alisa-standalone/S99zz-alisa-voice.off-jarvisd; mount -o remount,ro /; true")
	}
	j.log("Добавляю автозапуск…")
	if err := a.Push(payload("S98jarvisd"), "/tmp/S98jarvisd", 0o755, nil); err != nil {
		return err
	}
	// Копия скрипта автозапуска всегда лежит рядом с Джарвисом: на прошивках, где корень не
	// перемонтируется (Мини 3 после некоторых способов root), его ставят в автозапуск вручную.
	_, _ = run("cp /tmp/S98jarvisd " + remoteDir + "/S98jarvisd && chmod 755 " + remoteDir + "/S98jarvisd")
	if out, err := run(autostartScript(remoteDir)); err != nil {
		_, _ = run("rm -f /tmp/S98jarvisd")
		j.log("Корень прошивки не открылся на запись: %s", strings.TrimSpace(out))
		return fmt.Errorf("Джарвис установлен, но автозапуск добавить не удалось: корневой раздел колонки только для чтения (%v). "+
			"Добавьте в автозапуск своей прошивки скрипт %s/S98jarvisd (он запускается до S99quasar) и перезагрузите колонку", err, remoteDir)
	}
	if spotify.enabled() {
		// Soloist — отдельный проект; его сбой не отменяет установку Джарвиса, а только попадает в журнал
		freeKB := func() int {
			o, _, _ := a.Shell("df -k /data | awk 'NR==2{print $4}'", 10*time.Second)
			kb, _ := strconv.Atoi(strings.TrimSpace(o))
			return kb
		}
		if err := installSoloist(j, a, freeKB, spotify); err != nil {
			j.log("Spotify: не получилось — %v. Джарвис установлен; Soloist можно поставить позже, повторив установку.", err)
		}
	}
	_, _ = run("sync")
	j.log("Перезагружаю колонку — Алиса загрузится уже с микрофоном для Джарвиса (около минуты)…")
	_, _, _ = a.Shell("sync; reboot", 5*time.Second) // связь оборвётся — это нормально
	a.Close()
	url := "http://" + ip + ":8080/"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	time.Sleep(15 * time.Second)
	for ctx.Err() == nil {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				j.log("Готово! Джарвис запущен.")
				j.mu.Lock()
				j.url = url
				j.mu.Unlock()
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return errors.New("колонка перезагрузилась, но настройки Джарвиса не открылись за 4 минуты — проверьте, что она в сети")
}

func handleUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct{ IP string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	err := startJob(func(j *job) error {
		j.log("Подключаюсь к %s…", req.IP)
		a, err := DialADB(req.IP+":5555", 5*time.Second)
		if err != nil {
			return err
		}
		defer a.Close()
		out, _, _ := a.Shell("[ -x "+remoteDir+"/jarvisd.sh ] && echo y", 10*time.Second)
		if strings.TrimSpace(out) != "y" {
			return errors.New("Джарвис на этой колонке не установлен")
		}
		j.log("Удаляю Джарвиса и перезагружаю колонку — Алиса вернётся как была.")
		_, _, _ = a.Shell(remoteDir+"/jarvisd.sh uninstall", 30*time.Second) // в конце перезагрузка — связь оборвётся
		j.log("Готово.")
		return nil
	})
	if err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// autostartScript — положить S98jarvisd в /etc/init.d. Если там уже такой же (обновление или
// автозапуск добавлен вручную), корень не трогаем. Иначе перемонтируем «/» на запись: сначала
// обычным способом, потом с явным устройством (на части прошивок busybox иначе не находит раздел).
func autostartScript(remoteDir string) string {
	return "if cmp -s /tmp/S98jarvisd /etc/init.d/S98jarvisd; then rc=0; else " +
		"dev=$(awk '$2==\"/\" {print $1; exit}' /proc/mounts); " +
		"if mount -o remount,rw / 2>/tmp/jarvis-mount.err || mount -o remount,rw \"$dev\" / 2>>/tmp/jarvis-mount.err; then " +
		"cp /tmp/S98jarvisd /etc/init.d/S98jarvisd && chmod 755 /etc/init.d/S98jarvisd && sync; rc=$?; mount -o remount,ro / 2>/dev/null; " +
		"else cat /tmp/jarvis-mount.err; grep \" / \" /proc/mounts; rc=1; fi; fi; " +
		"rm -f /tmp/S98jarvisd /tmp/jarvis-mount.err " + remoteDir + "/disabled " + remoteDir + "/no-mic; (exit $rc)"
}
