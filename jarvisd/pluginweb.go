package main

// Страница «Плагины» и мастер знакомства: API панели.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

func (w *Web) pluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins", w.auth(w.pluginsList))
	mux.HandleFunc("GET /api/plugins/catalog", w.auth(w.pluginsCatalog))
	mux.HandleFunc("POST /api/plugins/install", w.auth(w.pluginsInstall))
	mux.HandleFunc("POST /api/plugins/upload", w.auth(w.pluginsUpload))
	mux.HandleFunc("POST /api/plugins/{id}/enable", w.auth(w.pluginsEnable))
	mux.HandleFunc("POST /api/plugins/{id}/settings", w.auth(w.pluginsSettings))
	mux.HandleFunc("DELETE /api/plugins/{id}", w.auth(w.pluginsRemove))
	mux.HandleFunc("POST /api/plugins/warned", w.auth(w.pluginsWarned))
	mux.HandleFunc("POST /api/onboarded", w.auth(w.onboarded))
}

func (w *Web) plugins() (*PluginHost, error) {
	if pluginHost == nil {
		return nil, errors.New("плагины не загружены")
	}
	return pluginHost, nil
}

func (w *Web) pluginsList(rw http.ResponseWriter, r *http.Request) {
	h, err := w.plugins()
	if err != nil {
		bad(rw, err.Error())
		return
	}
	free, _ := diskSpace(w.dataDir)
	writeJSON(rw, 200, map[string]any{"installed": h.List(), "arch": pluginArch(), "api": pluginAPI, "free_mb": free, "categories": pluginCategories,
		"warned": w.store.Get().PluginsWarned, "topic": pluginTopic, "repo": defaultRepo})
}

func (w *Web) pluginsCatalog(rw http.ResponseWriter, r *http.Request) {
	h, err := w.plugins()
	if err != nil {
		bad(rw, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	items, errText := h.Catalog(ctx, r.URL.Query().Get("fresh") == "1")
	writeJSON(rw, 200, map[string]any{"items": items, "error": errText})
}

func (w *Web) pluginsInstall(rw http.ResponseWriter, r *http.Request) {
	var req struct{ Repo, ID string }
	if readJSON(r, &req) != nil {
		bad(rw, "нет репозитория")
		return
	}
	h, err := w.plugins()
	if err != nil {
		bad(rw, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	m, err := h.InstallRepo(ctx, req.Repo, req.ID)
	if err != nil {
		bad(rw, shortErr(err))
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true, "plugin": m})
}

func (w *Web) pluginsUpload(rw http.ResponseWriter, r *http.Request) {
	h, err := w.plugins()
	if err != nil {
		bad(rw, err.Error())
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, pluginZipMax+1))
	if err != nil {
		bad(rw, shortErr(err))
		return
	}
	m, err := h.Install(raw, "file", false)
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true, "plugin": m})
}

func (w *Web) pluginsEnable(rw http.ResponseWriter, r *http.Request) {
	var req struct{ On bool }
	_ = readJSON(r, &req)
	h, err := w.plugins()
	if err == nil {
		err = h.SetEnabled(r.PathValue("id"), req.On)
	}
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Web) pluginsSettings(rw http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if readJSON(r, &req) != nil {
		bad(rw, "настройки не JSON")
		return
	}
	h, err := w.plugins()
	if err == nil {
		err = h.SetSettings(r.PathValue("id"), req)
	}
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Web) pluginsRemove(rw http.ResponseWriter, r *http.Request) {
	h, err := w.plugins()
	if err == nil {
		err = h.Uninstall(r.PathValue("id"))
	}
	if err != nil {
		bad(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"ok": true})
}

// pluginsWarned — «больше не показывать» предупреждение о неофициальных плагинах.
func (w *Web) pluginsWarned(rw http.ResponseWriter, r *http.Request) {
	_ = w.store.Update(func(s *Settings) error { s.PluginsWarned = true; return nil })
	writeJSON(rw, 200, map[string]any{"ok": true})
}

// onboarded — мастер знакомства пройден (или пропущен); ?again=1 — показать снова.
func (w *Web) onboarded(rw http.ResponseWriter, r *http.Request) {
	done := r.URL.Query().Get("again") != "1"
	_ = w.store.Update(func(s *Settings) error { s.Onboarded = done; return nil })
	writeJSON(rw, 200, map[string]any{"ok": true})
}

// pluginSummary — коротко для /api/state: какие плагины стоят и включены (панель прячет лишнее).
func pluginSummary() map[string]any {
	if pluginHost == nil {
		return map[string]any{"installed": []string{}, "enabled": []string{}}
	}
	inst, on := []string{}, []string{}
	for _, p := range pluginHost.List() {
		inst = append(inst, p.ID)
		if p.Enabled {
			on = append(on, p.ID)
		}
	}
	return map[string]any{"installed": inst, "enabled": on}
}
