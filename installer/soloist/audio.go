package soloist

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Выбор звукового устройства Soloist. В скриптах запуска (start.sh и scripts/soloist-service.sh) есть строка
// «export APULSE_PLAYBACK_DEVICE=…» для обычных колонок. На разных моделях работает разное устройство
// (на Станции 2 — sysdefault:CARD=<карта>, на других plug:default), поэтому строку обводим метками,
// а Tuner подбирает рабочее значение и переписывает только то, что между метками.
const (
	markBegin = "# jarvis-audio begin"
	markEnd   = "# jarvis-audio end"
	upstream  = "plug:default" // значение апстрима
)

var scriptFiles = []string{"start.sh", filepath.Join("scripts", "soloist-service.sh")}

// Прежние формы строки, которые мы умеем превращать в блок с метками: оригинал апстрима (без кавычек и в кавычках),
// блок прошлых версий установщика и правка fix-sound.sh.
var (
	markRe = regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(markBegin) + `\n(?:.*\n)*?[ \t]*` + regexp.QuoteMeta(markEnd))
	lineRe = regexp.MustCompile(`(?m)^([ \t]*)export APULSE_PLAYBACK_DEVICE=("plug:default"|plug:default|sysdefault:CARD=AMLAUGESOUND)[ \t]*$`)

	// legacyCardBlock — блок, который вставляли версии 0.9.67-spotify4/5: имя карты из /proc/asound/cards.
	legacyCardBlock = `CARD_NAME=$(awk -F'[][]' '/^[ 0-9]/ {print $2; exit}' /proc/asound/cards 2>/dev/null | tr -d ' ')
        if [ -n "$CARD_NAME" ]; then
            export APULSE_PLAYBACK_DEVICE="sysdefault:CARD=$CARD_NAME"
        else
            export APULSE_PLAYBACK_DEVICE=plug:default
        fi`

	deviceRe = regexp.MustCompile(`^[A-Za-z0-9_.:=,\-]{1,80}$`)
)

func wrap(indent, dev string) string {
	return indent + markBegin + "\n" + indent + "export APULSE_PLAYBACK_DEVICE=" + dev + "\n" + indent + markEnd
}

// withMarks возвращает текст с блоком меток и признак, что блок есть (нашли готовый или смогли сделать).
func withMarks(s string) (string, bool) {
	if markRe.MatchString(s) {
		return s, true
	}
	if i := strings.Index(s, legacyCardBlock); i >= 0 && strings.Count(s, legacyCardBlock) == 1 {
		ls := strings.LastIndex(s[:i], "\n") + 1
		indent := s[ls:i]
		if strings.TrimSpace(indent) == "" {
			return s[:ls] + wrap(indent, upstream) + s[i+len(legacyCardBlock):], true
		}
	}
	if m := lineRe.FindAllStringSubmatchIndex(s, -1); len(m) == 1 {
		indent := s[m[0][2]:m[0][3]]
		return s[:m[0][0]] + wrap(indent, upstream) + s[m[0][1]:], true
	}
	return s, false
}

func editScript(path string, f func(string) (string, bool)) (bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	out, ok := f(string(b))
	if !ok || out == string(b) {
		return false, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(out), st.Mode().Perm())
}

// patchAudioDevice обводит строку выбора устройства метками в скриптах распаковки Soloist (поведение апстрима не меняется).
// Возвращает true, если хоть один файл изменён. Незнакомый скрипт и отсутствие файла — не ошибка.
func patchAudioDevice(dir string) (bool, error) {
	changed := false
	for _, rel := range scriptFiles {
		ok, err := editScript(filepath.Join(dir, rel), withMarks)
		if err != nil {
			return changed, err
		}
		changed = changed || ok
	}
	return changed, nil
}

// SetAudioDevice записывает выбранное устройство в скрипты запуска Soloist, лежащие в root.
// Возвращает ошибку, если ни один скрипт не удалось найти или понять.
func SetAudioDevice(root, dev string) error {
	if !deviceRe.MatchString(dev) {
		return fmt.Errorf("недопустимое имя устройства: %q", dev)
	}
	done := false
	for _, rel := range scriptFiles {
		path := filepath.Join(root, rel)
		_, err := editScript(path, func(s string) (string, bool) {
			s, ok := withMarks(s)
			if !ok {
				return s, false
			}
			return markRe.ReplaceAllStringFunc(s, func(blk string) string {
				indent := markRe.FindStringSubmatch(blk)[1]
				return wrap(indent, dev)
			}), true
		})
		if err != nil {
			return err
		}
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), markBegin) {
			done = true
		}
	}
	if !done {
		return fmt.Errorf("в %s не нашёл строку выбора звукового устройства Soloist", root)
	}
	return nil
}
