package main

import (
	"github.com/newfpv/YaJavis/pluginsdk"
	"regexp"
	"strings"
)

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
var leadRe = regexp.MustCompile(`^(?:(?:эй|слушай|так|ну)\s+)?(?:джарвис\s+)?`)

func normText(s string) string {
	return strings.Join(strings.Fields(nonWord.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, "ё", "е")), " ")), " ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
func shortErr(err error) string {
	s := err.Error()
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
func main() {
	p := pluginsdk.New()
	registerYandex(p)
	initHA := registerHomeassistant(p)
	initX := registerXiaomi(p)
	initSL := registerSmartlife(p)
	p.OnInit(func(in pluginsdk.Init) {
		initHA(in)
		initX(in)
		initSL(in)
		p.Status("Подключения умного дома")
	})
	p.Run()
}
