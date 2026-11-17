package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

func (h *HomeAssistant) stopVacuumBase(ctx context.Context, vacuum string, entities []HAEntity) {
	devices, err := h.deviceEntities(ctx)
	if err != nil || devices[vacuum] == "" {
		return
	}
	actions := []string{"stop_working"}
	for _, e := range entities {
		if devices[e.ID] != devices[vacuum] || !strings.Contains(e.ID, "_base_station_working_status_") {
			continue
		}
		var state struct {
			Mode *int `json:"mode"`
		}
		if json.Unmarshal([]byte(e.State), &state) == nil && state.Mode != nil {
			switch *state.Mode {
			case 0:
				return
			case 1:
				actions = []string{"stop_dry"}
			case 3:
				actions = []string{"stop_mop_wash"}
			}
		}
	}
	for _, action := range actions {
		for _, e := range entities {
			if devices[e.ID] == devices[vacuum] && strings.HasPrefix(e.ID, "button.") && strings.Contains(e.ID, "_"+action+"_a_") {
				_ = h.request(ctx, http.MethodPost, "services/button/press", map[string]any{"entity_id": e.ID}, nil)
			}
		}
	}
}
