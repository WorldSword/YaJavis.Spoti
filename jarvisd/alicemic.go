package main

// Микрофон приходит от vqe_tap.so, которую подгружает maind --platformd по нашему профилю запуска
// (launch.json подкладывает bind-mount'ом jarvisd.sh boot из S98jarvisd). quasar_launcher2 читает профиль
// один раз при старте. На Lite Алиса стартует из S99quasar — после нас, всё сходится. На Midi/Mini
// её скрипт S97quasar идёт раньше S98jarvisd: Алиса уже запущена со штатным профилем, и микрофона нет,
// пока её не перезапустить. WatchAliceMic замечает это после загрузки и один раз перезапускает Алису.

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	aliceLaunch      = "/system/vendor/quasar/launch.json"
	aliceRestartMark = "/tmp/jarvisd/alice-mic-restarted" // /tmp в памяти: не больше одного раза за загрузку
	aliceRunScript   = "/system/vendor/quasar/run.sh"
	// pkill в прошивке нет (даже штатный «S99quasar stop» на нём спотыкается) — killall есть
	aliceRestartShell = "killall quasar_launcher2; i=0; while pidof maind >/dev/null && [ $i -lt 20 ]; do sleep 0.5; i=$((i+1)); done; " +
		"killall -9 maind 2>/dev/null; sleep 1; start-stop-daemon -S -b -x " + aliceRunScript
)

// aliceMissesTap — профиль с vqe_tap.so подложен, Алиса (maind --platformd) работает, но библиотеки в ней нет:
// значит, она запустилась раньше, чем подложили профиль.
func aliceMissesTap(mounts string, platformdMaps []string) bool {
	if !strings.Contains(mounts, " "+aliceLaunch+" ") || len(platformdMaps) == 0 {
		return false
	}
	for _, m := range platformdMaps {
		if strings.Contains(m, "vqe_tap") {
			return false
		}
	}
	return true
}

// platformdMaps — карты памяти процессов maind --platformd.
func platformdMaps() []string {
	var out []string
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		cmd, err := os.ReadFile(d + "/cmdline")
		if err != nil || !strings.Contains(string(cmd), "maind") || !strings.Contains(string(cmd), "--platformd") {
			continue
		}
		if m, err := os.ReadFile(d + "/maps"); err == nil {
			out = append(out, string(m))
		}
	}
	return out
}

func WatchAliceMic(mic *Mic, dataDir string) {
	time.Sleep(25 * time.Second) // Алиса и tap успевают подняться после загрузки
	if mic.Online() {
		return
	}
	defer func() { // так и не заработал — разобрать, на каком шаге оборвалось (журнал и панель)
		if !mic.Online() {
			logMicDiagnosis(mic, dataDir)
		}
	}()
	mounts, _ := os.ReadFile("/proc/mounts")
	if !aliceMissesTap(string(mounts), platformdMaps()) {
		time.Sleep(20 * time.Second) // дать шумодаву время на первые кадры
		return
	}
	if _, err := os.Stat(aliceRestartMark); err == nil {
		log.Printf("микрофон: Алиса снова без vqe_tap.so — второй раз не перезапускаю, нужна перезагрузка колонки")
		return
	}
	if _, err := os.Stat(aliceRunScript); err != nil { // другая прошивка: без скрипта запуска Алису не поднять обратно
		log.Printf("микрофон: Алиса запустилась раньше Джарвиса, а %s нет — перезапускать не рискую, перезагрузите колонку", aliceRunScript)
		return
	}
	_ = os.WriteFile(aliceRestartMark, []byte(time.Now().Format(time.RFC3339)), 0o644)
	log.Printf("микрофон: Алиса запустилась раньше Джарвиса (без vqe_tap.so) — перезапускаю Алису")
	if out, err := exec.Command("/bin/sh", "-c", aliceRestartShell).CombinedOutput(); err != nil {
		log.Printf("микрофон: перезапуск Алисы не удался: %v %s", err, strings.TrimSpace(string(out)))
		return
	}
	for i := 0; i < 60; i++ {
		time.Sleep(time.Second)
		if mic.Online() {
			log.Printf("микрофон: после перезапуска Алисы на связи (%d с)", i+1)
			return
		}
	}
	log.Printf("микрофон: после перезапуска Алисы звука всё ещё нет")
}
