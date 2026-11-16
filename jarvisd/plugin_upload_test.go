package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPluginUploadOverDefaultBodyLimit(t *testing.T) {
	h := testHost(t)
	old := pluginHost
	pluginHost = h
	defer func() {
		pluginHost = old
		for _, rt := range h.running() {
			rt.Stop()
		}
	}()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"plugin.json": `{"id":"large","name":"Large","version":"1","kind":"js","category":"other","main":"main.js"}`,
		"main.js":     "/*" + strings.Repeat("a", 384<<10) + "*/",
	} {
		w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= 256<<10 {
		t.Fatal("fixture too small")
	}
	w := &Web{store: &Store{s: defaultSettings()}}
	r := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", &buf)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Jarvis", "1")
	out := httptest.NewRecorder()
	w.guard(http.HandlerFunc(w.pluginsUpload)).ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatalf("large plugin rejected: %d %s", out.Code, out.Body.String())
	}
}
