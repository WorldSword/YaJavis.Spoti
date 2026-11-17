package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHomeAssistantControlAndUnknownRoom(t *testing.T) {
	var actions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPost {
			actions.Add(1)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_ = json.NewEncoder(w).Encode([]HAEntity{{ID: "light.test", State: "off", Attributes: map[string]any{"friendly_name": "Test"}}, {ID: "vacuum.test", State: "docked"}})
	}))
	defer srv.Close()
	cfg := HASettings{URL: srv.URL, Token: "test-token"}
	h := &HomeAssistant{config: func() HASettings { return cfg }, client: srv.Client()}
	if result := h.control(context.Background(), `{"entity_id":"light.test","action":"turn_on"}`); actions.Load() != 1 || strings.Contains(result, "недоступен") {
		t.Fatal(result, actions.Load())
	}
	before := actions.Load()
	result := h.control(context.Background(), `{"entity_id":"vacuum.test","action":"clean_room","room":"Unknown"}`)
	if actions.Load() != before {
		t.Fatal("unknown room started whole-apartment cleaning", result)
	}
}

func TestHomeAssistantPunctuationIntent(t *testing.T) {
	h := &HomeAssistant{config: func() HASettings { return HASettings{URL: "http://example.invalid", Token: "test"} }}
	if !h.vacuumIntent("Джарвис, останови пылесос!") {
		t.Fatal("punctuation lost during extraction")
	}
}
