package soloist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
}

type tarEntry struct {
	name, body, link string
	mode             int64
	typ              byte
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: typ, Linkname: e.link, Size: int64(len(e.body))}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestExtractTarGzRejectsEscapes(t *testing.T) {
	bad := [][]tarEntry{
		{{name: "../evil", body: "x", mode: 0o644}},
		{{name: "/abs/evil", body: "x", mode: 0o644}},
		{{name: "a/../../evil", body: "x", mode: 0o644}},
		{{name: "lnk", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		{{name: "lnk", typ: tar.TypeSymlink, link: "../../outside"}},
	}
	for i, es := range bad {
		dst := t.TempDir()
		if err := extractTarGz(bytes.NewReader(makeTarGz(t, es)), dst, 1<<20); err == nil {
			t.Errorf("случай %d: архив с выходом за папку принят", i)
		}
	}
}

func TestExtractTarGzOK(t *testing.T) {
	dst := t.TempDir()
	data := makeTarGz(t, []tarEntry{
		{name: "soloist-station-lite/", typ: tar.TypeDir, mode: 0o755},
		{name: "soloist-station-lite/install.sh", body: "echo hi", mode: 0o755},
		{name: "soloist-station-lite/lib64/libx.so", typ: tar.TypeSymlink, link: "libx.so.1"},
	})
	if err := extractTarGz(bytes.NewReader(data), dst, 1<<20); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dst, "soloist-station-lite", "install.sh"))
	if err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("install.sh: %v %v", st, err)
	}
	if got, _ := findInstallScript(dst); got != filepath.Join(dst, "soloist-station-lite") {
		t.Errorf("findInstallScript = %q", got)
	}
}

func TestExtractTarGzSizeCap(t *testing.T) {
	data := makeTarGz(t, []tarEntry{{name: "big", body: strings.Repeat("a", 5000), mode: 0o644}})
	if err := extractTarGz(bytes.NewReader(data), t.TempDir(), 1000); err == nil {
		t.Error("превышение лимита размера принято")
	}
}

// releaseServer отдаёт «релиз» Soloist: JSON с asset и сам архив.
func releaseServer(t *testing.T, archive []byte, digest string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			asset := map[string]any{"name": "soloist-station-lite.tar.gz", "size": len(archive), "browser_download_url": srv.URL + "/dl/soloist-station-lite.tar.gz"}
			if digest != "" {
				asset["digest"] = digest
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{"tag_name": "v1.3.13", "assets": []any{
				map[string]any{"name": "soloist-station-lite.yampkg", "browser_download_url": srv.URL + "/dl/x"}, asset}})
			return
		}
		_, _ = rw.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testInstaller(t *testing.T, srv *httptest.Server, _ string, run Exec) *Installer {
	t.Helper()
	i := New(run, "test")
	i.API = srv.URL + "/repos/Horohi/soloist-station-lite/releases/latest"
	i.Tmp = t.TempDir()
	i.Get = func(ctx context.Context, url string) (*http.Response, error) { return srv.Client().Get(url) }
	return i
}

func waitInstall(t *testing.T, i *Installer) map[string]any {
	t.Helper()
	for n := 0; n < 200; n++ {
		if s := i.Status(); s["state"] != "running" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("установка не завершилась")
	return nil
}

func goodArchive(t *testing.T) []byte {
	return makeTarGz(t, []tarEntry{
		{name: "soloist-station-lite/install.sh", body: "#!/bin/sh\n", mode: 0o755},
		{name: "soloist-station-lite/bin/soloist", body: "bin", mode: 0o755},
	})
}

func TestSoloistInstallRuns(t *testing.T) {
	arch := goodArchive(t)
	sum := sha256.Sum256(arch)
	srv := releaseServer(t, arch, "sha256:"+hex.EncodeToString(sum[:]))
	var mu sync.Mutex
	var calls []call
	i := testInstaller(t, srv, "arm64", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, call{name, args})
		mu.Unlock()
		return nil, nil
	})
	done := make(chan struct{})
	i.OnDone = func() { close(done) }
	if err := i.Start(Opts{APIKey: "spak_abc", DeviceName: "Станция", ClientID: "cid", ClientSecret: "sec"}); err != nil {
		t.Fatal(err)
	}
	if s := waitInstall(t, i); s["state"] != "done" {
		t.Fatalf("состояние %v", s)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnDone не вызван")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].name != "sh" {
		t.Fatalf("вызовы: %+v", calls)
	}
	joined := strings.Join(calls[0].args, "|")
	for _, want := range []string{"spak_abc", "Станция", "cid", "sec"} {
		if !strings.Contains(joined, want) {
			t.Errorf("в аргументах нет %q: %s", want, joined)
		}
	}
	if strings.Contains(calls[0].args[1], "spak_abc") {
		t.Error("ключ вставлен прямо в текст команды sh -c, а не передан аргументом")
	}
}

func TestSoloistInstallBadDigest(t *testing.T) {
	srv := releaseServer(t, goodArchive(t), "sha256:"+strings.Repeat("0", 64))
	ran := false
	i := testInstaller(t, srv, "arm64", func(ctx context.Context, name string, args ...string) ([]byte, error) { ran = true; return nil, nil })
	if err := i.Start(Opts{APIKey: "spak_abc"}); err != nil {
		t.Fatal(err)
	}
	s := waitInstall(t, i)
	if s["state"] != "error" || !strings.Contains(s["error"].(string), "SHA-256") || ran {
		t.Errorf("ждали ошибку SHA-256 без запуска install.sh: %v ran=%v", s, ran)
	}
}

func TestSoloistInstallValidatesInput(t *testing.T) {
	srv := releaseServer(t, goodArchive(t), "")
	i := testInstaller(t, srv, "arm64", func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil })
	for _, o := range []Opts{
		{},
		{APIKey: "a$(reboot)"},
		{APIKey: "spak_abc", DeviceName: "x\"y"},
		{APIKey: "spak_abc", ClientID: "a`b`"},
	} {
		if err := i.Start(o); err == nil {
			t.Errorf("принято %+v", o)
		}
	}
}

func TestSoloistInstallInstallScriptFails(t *testing.T) {
	srv := releaseServer(t, goodArchive(t), "")
	i := testInstaller(t, srv, "arm64", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_ = os.WriteFile(args[len(args)-1], []byte("ERROR: Недостаточно места в /data"), 0o644)
		return nil, os.ErrInvalid
	})
	_ = i.Start(Opts{APIKey: "spak_abc"})
	s := waitInstall(t, i)
	if s["state"] != "error" || !strings.Contains(s["error"].(string), "Недостаточно места") {
		t.Errorf("в ошибке должен быть вывод install.sh: %v", s)
	}
}
