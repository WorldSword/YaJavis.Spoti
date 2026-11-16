package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func standaloneRoot(t *testing.T) string {
	t.Helper()
	root := fakeSoloistRoot(t)
	for _, f := range []string{"start.sh", "stop.sh"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func postTune(w *Web) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	w.spotifyTune(rec, httptest.NewRequest(http.MethodPost, "/api/spotify/tune", nil))
	return rec
}

func TestPostSpotifyTuneWithoutSoloist(t *testing.T) {
	w := spotifyWeb(t, "", nil)
	if rec := postTune(w); rec.Code != 400 {
		t.Errorf("без Soloist: код %d, ждали 400", rec.Code)
	}
}

func TestPostSpotifyTuneStartsAndReportsState(t *testing.T) {
	calls := []spotifyCall{}
	w := spotifyWeb(t, standaloneRoot(t), fakeSpotifyExec(&calls))
	w.engine.spotify.Tuner().Sleep = func(time.Duration) {}
	if rec := postTune(w); rec.Code != 202 {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	tune, ok := w.spotifyState()["tune"]
	if !ok || tune == nil {
		t.Fatalf("в состоянии нет tune: %v", w.spotifyState())
	}
	for n := 0; n < 300 && w.engine.spotify.Tuner().Status().State == "running"; n++ {
		time.Sleep(10 * time.Millisecond)
	}
	// фиктивная колонка ничего не играет: подбор честно заканчивается ошибкой, а не зависает
	if st := w.engine.spotify.Tuner().Status(); st.State != "error" {
		t.Errorf("статус после подбора: %+v", st)
	}
}
