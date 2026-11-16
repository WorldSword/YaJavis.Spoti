package main

import "testing"

// «Джарвис, таймер на одна минута» без глагола уходил модели, и та отвечала, что таймеры не умеет.
func TestAliceSkillBareTimer(t *testing.T) {
	for _, c := range []struct {
		s  string
		ok bool
	}{{"таймер на одна минута", true}, {"таймер на 1 минуту", true}, {"будильник на семь утра", true},
		{"поставь таймер на одну минуту", true}, {"какая погода", false}, {"расскажи про таймер в духовке", false}} {
		if _, ok := AliceSkillFor(canonName("Джарвис, "+c.s), true); ok != c.ok {
			t.Errorf("%s → %v, ждали %v", c.s, ok, c.ok)
		}
	}
}
