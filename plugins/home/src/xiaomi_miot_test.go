package main

import (
	"strings"
	"testing"
)

func TestMiotTypesFromStream(t *testing.T) {
	cat := `{"x":[1,2],"instances":[{"model":"a.v1","type":"urn:a:1","version":1},{"model":"b.v1","type":"urn:b:1","version":1},{"model":"a.v1","type":"urn:a:2","version":2}]}`
	got := miotTypesFrom(strings.NewReader(cat), map[string]bool{"a.v1": true})
	if len(got) != 1 || got["a.v1"] != "urn:a:2" {
		t.Fatalf("нужна только своя модель последней версии: %v", got)
	}
}
