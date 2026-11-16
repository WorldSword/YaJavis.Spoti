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
	defaultDeviceLine = "export APULSE_PLAYBACK_DEVICE=plug:default"

	// cardNameShell кладёт в CARD_NAME имя первой звуковой карты из /proc/asound/cards («0 [AMLAUGESOUND  ]: …»).
	cardNameShell = `CARD_NAME=$(awk -F'[][]' '/^[ 0-9]/ {print $2; exit}' /proc/asound/cards 2>/dev/null | tr -d ' ')`
)

var cardDeviceBlock = cardNameShell + `
        if [ -n "$CARD_NAME" ]; then
            export APULSE_PLAYBACK_DEVICE="sysdefault:CARD=$CARD_NAME"
        else
            export APULSE_PLAYBACK_DEVICE=plug:default
        fi`

// patchAudioDevice правит <dir>/scripts/soloist-service.sh. Возвращает true, если файл изменён.
// Незнакомый скрипт (строки нет или она не одна) и отсутствие файла — не ошибка: ничего не трогаем.
func patchAudioDevice(dir string) (bool, error) {
	path := filepath.Join(dir, "scripts", "soloist-service.sh")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := string(b)
	if strings.Contains(s, cardNameShell) || strings.Count(s, defaultDeviceLine) != 1 { // уже исправлен или незнакомый скрипт
		return false, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(strings.Replace(s, defaultDeviceLine, cardDeviceBlock, 1)), st.Mode().Perm())
}
