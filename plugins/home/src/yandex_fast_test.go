package main

import (
	"encoding/json"
	"testing"
)

const testCatalog = `{"rooms":[{"id":"r1","name":"Зал"},{"id":"r2","name":"Кухня"},{"id":"r3","name":"Спальня"}],
"devices":[
 {"id":"spk","name":"Станция","type":"devices.types.smart_speaker.yandex.station.micro","room":"r1","capabilities":[]},
 {"id":"l1","name":"Свет","type":"devices.types.light","room":"r1","capabilities":[{"type":"devices.capabilities.on_off"}]},
 {"id":"l2","name":"Лента стол","type":"devices.types.light","room":"r1","capabilities":[{"type":"devices.capabilities.on_off"}]},
 {"id":"l3","name":"Люстра","type":"devices.types.light","room":"r2","capabilities":[{"type":"devices.capabilities.on_off"}]},
 {"id":"l4","name":"Лента","type":"devices.types.light","room":"r3","capabilities":[{"type":"devices.capabilities.on_off"}]},
 {"id":"s1","name":"Розетка","type":"devices.types.socket","room":"r2","capabilities":[{"type":"devices.capabilities.on_off"}]},
 {"id":"v1","name":"Mijia Robot Vacuum 5","type":"devices.types.vacuum_cleaner","room":"r2","capabilities":[{"type":"devices.capabilities.on_off"}]},
 {"id":"ac","name":"Кондиционер","type":"devices.types.thermostat.ac","room":"r1","capabilities":[{"type":"devices.capabilities.on_off"}]}],
"groups":[],"scenarios":[{"id":"sc1","name":"Я дома"}]}`

func TestPlanCommand(t *testing.T) {
	var c iotCatalog
	if err := json.Unmarshal([]byte(testCatalog), &c); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"Джарвис, выключи свет.":  "off:l1",
		"включи свет на кухне":    "on:l3",
		"выключи весь свет":       "off:l1,l2,l3,l4",
		"Выключи стол":            "off:l2",
		"включи ленту":            "on:l4",
		"включи ленту в спальне":  "on:l4",
		"выключи розетку":         "off:s1",
		"запусти сценарий я дома": "scenario:sc1",
		"включи телевизор":        "",
		"запусти пылесос":         "on:v1",
		"останови пылесос":        "off:v1",
		"выключи кондей":          "off:ac",
		"расскажи анекдот":        "",
	}
	for text, want := range cases {
		got := ""
		if m := fastCmdRe.FindStringSubmatch(normText(text)); m != nil {
			if p := planCommand(&c, m[1], m[2]); p != nil {
				if p.scenario != "" {
					got = "scenario:" + p.scenario
				} else {
					got = map[bool]string{true: "on:", false: "off:"}[p.on]
					for i, id := range p.ids {
						if i > 0 {
							got += ","
						}
						got += id
					}
				}
			}
		}
		if got != want {
			t.Errorf("%q: получилось %q, ждали %q", text, got, want)
		}
	}
	if m := colorRe.FindStringSubmatch(normText("Джарвис, сделай ленту красной")); m == nil || m[2] != "красн" || m[1] != "ленту" {
		t.Errorf("цвет не распознан: %v", m)
	}
	if m := colorRe.FindStringSubmatch(normText("поставь лампу тёплым светом")); m == nil || m[2] != "тепл" {
		t.Errorf("тёплый не распознан: %v", m)
	}
}
