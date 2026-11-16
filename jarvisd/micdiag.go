package main

// Самодиагностика микрофона: почему Джарвис не слышит колонку. Нужна прежде всего на моделях, которых
// у нас нет под рукой (Стрит, Мини 3…): тестеру достаточно прислать вывод `jarvisd diag-mic`
// или строку «микрофон: …» из журнала — по ней видно, на каком шаге оборвалась цепочка
// launch.json → maind --platformd → vqe_tap.so → YandexVqeC_GetOutputChannelData → канал → сокет.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const vqeSymbol = "YandexVqeC_GetOutputChannelData"

var (
	micWhyMu sync.Mutex
	micWhy   string // коротко для панели: почему нет звука (пусто — не проверяли или всё в порядке)
)

func setMicWhy(s string) {
	micWhyMu.Lock()
	micWhy = s
	micWhyMu.Unlock()
}

func micWhyText() string {
	micWhyMu.Lock()
	defer micWhyMu.Unlock()
	return micWhy
}

type platformdProc struct {
	pid     string
	cmd     string
	environ string
	maps    string
}

func platformdProcs() []platformdProc {
	var out []platformdProc
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		cmd, err := os.ReadFile(d + "/cmdline")
		if err != nil || !bytes.Contains(cmd, []byte("maind")) || !bytes.Contains(cmd, []byte("--platformd")) {
			continue
		}
		env, _ := os.ReadFile(d + "/environ")
		maps, _ := os.ReadFile(d + "/maps")
		out = append(out, platformdProc{filepath.Base(d), strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " ")),
			strings.ReplaceAll(string(env), "\x00", "\n"), string(maps)})
	}
	return out
}

// mappedLibs — файлы библиотек, загруженных в процесс.
func mappedLibs(maps string) []string {
	set := map[string]bool{}
	for _, l := range strings.Split(maps, "\n") {
		f := strings.Fields(l)
		if len(f) >= 6 && strings.HasPrefix(f[5], "/") && strings.Contains(f[5], ".so") {
			set[f[5]] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// fileHas — есть ли строка в файле (читаем кусками: библиотеки quasar по десятку мегабайт, памяти мало).
func fileHas(path, needle string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	buf := make([]byte, 64<<10+len(needle))
	keep := 0
	for {
		n, err := io.ReadFull(r, buf[keep:])
		if bytes.Contains(buf[:keep+n], []byte(needle)) {
			return true
		}
		if err != nil {
			return false
		}
		keep = copy(buf, buf[keep+n-len(needle):keep+n])
	}
}

// elfMachine — разрядность и архитектура ELF («32-бит ARM», «64-бит ARM»…), чтобы сравнить vqe_tap.so с maind.
func elfMachine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := make([]byte, 20)
	if _, err := io.ReadFull(f, h); err != nil || string(h[:4]) != "\x7fELF" {
		return ""
	}
	bits := map[byte]string{1: "32", 2: "64"}[h[4]]
	m := int(h[18]) | int(h[19])<<8
	name := map[int]string{40: "ARM", 183: "ARM64", 3: "x86", 62: "x86-64"}[m]
	if name == "" {
		name = fmt.Sprintf("machine %d", m)
	}
	return bits + "-бит " + name
}

// micDiagnose — разбор по шагам. why — одна фраза для панели и журнала, lines — подробности для тестера.
func micDiagnose(mic *Mic, dataDir string) (why string, lines []string) {
	add := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	fail := func(f string, a ...any) {
		if why == "" {
			why = fmt.Sprintf(f, a...)
		}
		add("✗ "+f, a...)
	}
	rel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	add("колонка: %s, ядро %s, jarvisd %s/%s %s", stationModel().Name, strings.TrimSpace(string(rel)), runtime.GOOS, runtime.GOARCH, version)
	if mic != nil {
		mic.mu.Lock()
		seen := mic.Seen
		mic.mu.Unlock()
		switch {
		case time.Since(seen) < 3*time.Second:
			add("✓ звук идёт")
		case seen.IsZero():
			add("✗ с запуска Джарвиса не пришло ни одного кадра")
		default:
			add("✗ звук был, пропал %s назад", time.Since(seen).Round(time.Second))
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "no-mic")); err == nil {
		fail("микрофон выключен вручную: есть файл %s/no-mic", dataDir)
		return
	}
	tap := filepath.Join(dataDir, "vqe_tap.so")
	if _, err := os.Stat(tap); err != nil {
		fail("нет %s — переустановите Джарвиса", tap)
		return
	}
	if _, err := os.Stat(aliceLaunch); err != nil {
		fail("нет %s — у этой прошивки другой запуск Алисы", aliceLaunch)
		return
	}
	mounts, _ := os.ReadFile("/proc/mounts")
	if !strings.Contains(string(mounts), " "+aliceLaunch+" ") {
		tmp := filepath.Join(os.TempDir(), "jarvisd-launch-check.json")
		err := patchLaunch(aliceLaunch, tmp, tap, "/tmp/jarvisd/mic.sock")
		_ = os.Remove(tmp)
		if err != nil {
			fail("профиль запуска Алисы не подменить: %v", err)
		} else {
			fail("профиль запуска Алисы не подменён (bind-mount не сделан) — перезагрузите колонку")
		}
		return
	}
	add("✓ профиль запуска Алисы подменён")
	procs := platformdProcs()
	if len(procs) == 0 {
		fail("не найден процесс maind --platformd — Алиса не запущена или устроена иначе")
		return
	}
	p := procs[0]
	add("  процесс %s: %s", p.pid, p.cmd)
	if !strings.Contains(p.environ, "vqe_tap") {
		fail("Алиса запущена без нашего профиля (раньше Джарвиса) — перезагрузите колонку")
		return
	}
	if !strings.Contains(p.maps, "vqe_tap") {
		exe, _ := os.Readlink("/proc/" + p.pid + "/exe")
		fail("vqe_tap.so не загрузилась в Алису: у maind %s, у библиотеки %s", elfMachine("/proc/"+p.pid+"/exe"), elfMachine(tap))
		add("  maind: %s", exe)
		return
	}
	add("✓ vqe_tap.so загружена в Алису")
	var vqeLib string
	for _, l := range mappedLibs(p.maps) {
		if !strings.Contains(l, "vqe_tap") && fileHas(l, vqeSymbol) {
			vqeLib = l
			break
		}
	}
	if vqeLib == "" {
		exe := "/proc/" + p.pid + "/exe"
		if fileHas(exe, vqeSymbol) {
			fail("%s вшита в сам maind (не в библиотеку) — перехват через LD_PRELOAD не сработает", vqeSymbol)
		} else {
			fail("в Алисе нет %s — у этой прошивки другой шумодав, нужен другой перехват", vqeSymbol)
		}
		var libs []string
		for _, l := range mappedLibs(p.maps) {
			if b := filepath.Base(l); strings.Contains(strings.ToLower(b), "vqe") || strings.Contains(strings.ToLower(b), "audio") ||
				strings.Contains(strings.ToLower(b), "speechkit") || strings.Contains(strings.ToLower(b), "aec") {
				libs = append(libs, b)
			}
		}
		if len(libs) > 0 {
			add("  звуковые библиотеки в Алисе: %s", strings.Join(libs, ", "))
		}
		return
	}
	add("✓ шумодав Яндекса: %s", vqeLib)
	sock := "/tmp/jarvisd/mic.sock"
	if mic != nil {
		sock = mic.path
	}
	seenRaw, err := os.ReadFile(sock + ".seen")
	if err != nil || len(bytes.TrimSpace(seenRaw)) == 0 {
		fail("перехват стоит, но Алиса ни разу не вызвала %s — звук обрабатывается другим путём (или vqe_tap.so старая: переустановите Джарвиса установщиком)", vqeSymbol)
		return
	}
	want := "6 0"
	if b, err := os.ReadFile(filepath.Join(dataDir, "mic-channel")); err == nil {
		want = strings.Replace(strings.TrimSpace(string(b)), ":", " ", 1)
	}
	var chans []string
	found := false
	for _, l := range strings.Split(strings.TrimSpace(string(seenRaw)), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 {
			chans = append(chans, fmt.Sprintf("тип %s канал %s (%s отсчётов)", f[0], f[1], f[2]))
			if f[0]+" "+f[1] == want {
				found = true
			}
		}
	}
	add("  каналы шумодава: %s", strings.Join(chans, "; "))
	if !found {
		fail("канала %s нет — на этой прошивке звук в другом канале: запишите «тип:канал» в %s/mic-channel и перезагрузите колонку",
			strings.Replace(want, " ", ":", 1), dataDir)
		return
	}
	add("✓ канал %s есть", strings.Replace(want, " ", ":", 1))
	if _, err := os.Stat(sock); err != nil {
		fail("нет сокета %s — Джарвис не слушает микрофон", sock)
		return
	}
	if mic != nil && !mic.Online() {
		fail("цепочка цела, но кадры не доходят до Джарвиса — пришлите этот отчёт разработчику")
	}
	return why, lines
}

// logMicDiagnosis — разбор в журнал и короткая причина в панель.
func logMicDiagnosis(mic *Mic, dataDir string) {
	why, lines := micDiagnose(mic, dataDir)
	setMicWhy(why)
	if why == "" {
		return
	}
	for _, l := range lines {
		log.Printf("микрофон: %s", l)
	}
}
