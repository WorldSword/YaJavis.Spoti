package main

import (
	"slices"
	"testing"
)

func tuyaDevs() []TuyaDevice {
	sw := func(codes ...string) []TuyaFunc {
		var out []TuyaFunc
		for _, c := range codes {
			out = append(out, TuyaFunc{Code: c, Type: "Boolean"})
		}
		return out
	}
	return []TuyaDevice{
		{ID: "1", Name: "Выключатель зал", Room: "Зал", Category: "kg", Funcs: sw("switch_1", "switch_2")},
		{ID: "2", Name: "Компьютер розетка ", Room: "Зал", Category: "cz", Funcs: sw("switch_1", "child_lock")},
		{ID: "3", Name: "Кондей", Room: "Зал", Category: "cz", Funcs: sw("switch_1", "child_lock")},
		{ID: "4", Name: "Кондиционер", Room: "Зал", Category: "kt", Funcs: sw("switch")},
		{ID: "5", Name: "Лампа", Room: "Зал", Category: "dj", Funcs: sw("switch_led", "do_not_disturb")},
		{ID: "6", Name: "Подсветка стол", Room: "Зал", Category: "dd", Funcs: sw("switch_led")},
		{ID: "7", Name: "Выключатель кухня", Room: "Кухня", Category: "kg", Funcs: sw("switch_1", "switch_2", "switch_3")},
		{ID: "8", Name: "Маша розетка", Room: "Спальня", Category: "cz", Funcs: sw("switch_1", "child_lock")},
	}
}

func TestTuyaPick(t *testing.T) {
	devs := tuyaDevs()
	ids := func(ds []TuyaDevice) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.ID)
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		cmd  string
		on   bool
		want []string // nil — не про Smart Life
	}{
		{"Джарвис, выключи свет.", false, []string{"1", "5", "6"}}, // весь свет в зале, а не «подсветка»
		{"включи свет на кухне", true, []string{"7"}},
		{"выключи весь свет", false, []string{"1", "5", "6", "7"}},
		{"выключи подсветку", false, []string{"6"}},
		{"включи лампу", true, []string{"5"}},
		{"выключи компьютер", false, []string{"2"}},
		{"выключи кондей", false, []string{"3"}},
		{"включи кондиционер", true, []string{"4"}},
		{"выключи машину розетку", false, []string{"8"}},
		{"включи музыку", false, nil},
		{"какая погода", false, nil},
	} {
		on, got, ok := tuyaPick(c.cmd, devs, "Зал")
		if c.want == nil {
			if ok {
				t.Errorf("%q: не должно быть Smart Life, выбрано %v", c.cmd, ids(got))
			}
			continue
		}
		if !ok || on != c.on || !slices.Equal(ids(got), c.want) {
			t.Errorf("%q: ok=%v on=%v выбрано %v, ждали %v", c.cmd, ok, on, ids(got), c.want)
		}
	}
}
