package soloist

import (
	"os"
	"path/filepath"
	"strings"
)

// На Станции 2 Soloist не может открыть плоский plug:default (apulse пишет «No such device or address»),
// а прямой профиль карты sysdefault:CARD=<имя> открывается и играет. Поэтому перед установкой
// правим в служебном скрипте Soloist строку выбора устройства для «обычных» колонок.
const (

	// cardNameShell кладёт в CARD_NAME имя первой звуковой карты из /proc/asound/cards («0 [AMLAUGESOUND  ]: …»).
	cardNameShell = `CARD_NAME=$(awk -F'[][]' '/^[ 0-9]/ {print $2; exit}' /proc/asound/cards 2>/dev/null | tr -d ' ')`
)

// Строка выбора устройства: в служебном скрипте без кавычек, в start.sh в кавычках.
var defaultDeviceLines = []string{"export APULSE_PLAYBACK_DEVICE=plug:default", `export APULSE_PLAYBACK_DEVICE="plug:default"`}

var cardDeviceBlock = cardNameShell + `
        if [ -n "$CARD_NAME" ]; then
            export APULSE_PLAYBACK_DEVICE="sysdefault:CARD=$CARD_NAME"
        else
            export APULSE_PLAYBACK_DEVICE=plug:default
        fi`

// patchAudioDevice правит служебный скрипт (scripts/soloist-service.sh) и старый запуск (start.sh) в <dir>.
// Возвращает true, если хоть один файл изменён. Незнакомый скрипт (строки нет или она не одна)
// и отсутствие файла — не ошибка: ничего не трогаем.
func patchAudioDevice(dir string) (bool, error) {
	changed := false
	for _, rel := range []string{filepath.Join("scripts", "soloist-service.sh"), "start.sh"} {
		ok, err := patchAudioFile(filepath.Join(dir, rel))
		if err != nil {
			return changed, err
		}
		changed = changed || ok
	}
	return changed, nil
}

func patchAudioFile(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := string(b)
	if strings.Contains(s, cardNameShell) { // уже исправлен
		return false, nil
	}
	line := ""
	for _, l := range defaultDeviceLines {
		if strings.Count(s, l) == 1 {
			line = l
		}
	}
	if line == "" { // незнакомый скрипт
		return false, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(strings.Replace(s, line, cardDeviceBlock, 1)), st.Mode().Perm())
}
