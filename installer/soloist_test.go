package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gz(t *testing.T, plain string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(plain))
	_ = w.Close()
	return b.Bytes()
}

func soloistServer(t *testing.T, archive []byte, digest string, name string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			a := map[string]any{"name": name, "browser_download_url": srv.URL + "/dl/" + name}
			if digest != "" {
				a["digest"] = digest
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.3.13", "assets": []any{
				map[string]any{"name": "soloist-station-lite.yampkg", "browser_download_url": srv.URL + "/dl/x"}, a}})
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchSoloistOK(t *testing.T) {
	archive := gz(t, "TARDATA")
	sum := sha256.Sum256(archive)
	srv := soloistServer(t, archive, "sha256:"+hex.EncodeToString(sum[:]), "soloist-station-lite.tar.gz")
	tar, tag, err := fetchSoloist(context.Background(), srv.Client(), srv.URL+"/repos/Horohi/soloist-station-lite/releases/latest")
	if err != nil || tag != "v1.3.13" || string(tar) != "TARDATA" {
		t.Fatalf("tar=%q tag=%q err=%v", tar, tag, err)
	}
}

func TestFetchSoloistBadDigest(t *testing.T) {
	srv := soloistServer(t, gz(t, "x"), "sha256:"+strings.Repeat("0", 64), "soloist-station-lite.tar.gz")
	if _, _, err := fetchSoloist(context.Background(), srv.Client(), srv.URL+"/repos/Horohi/soloist-station-lite/releases/latest"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("ждали ошибку SHA-256, получили %v", err)
	}
}

func TestFetchSoloistNoAsset(t *testing.T) {
	srv := soloistServer(t, gz(t, "x"), "", "other.tar.gz")
	if _, _, err := fetchSoloist(context.Background(), srv.Client(), srv.URL+"/repos/Horohi/soloist-station-lite/releases/latest"); err == nil || !strings.Contains(err.Error(), "soloist-station-lite.tar.gz") {
		t.Errorf("ждали сообщение про отсутствующий файл, получили %v", err)
	}
}

func TestSoloistOptsValidate(t *testing.T) {
	ok := SoloistOpts{Key: "spak_abc", Name: "Станция", ClientID: "cid", ClientSecret: "sec"}
	if err := ok.validate(); err != nil {
		t.Errorf("корректные настройки отклонены: %v", err)
	}
	for _, o := range []SoloistOpts{{Key: "a$(x)"}, {Key: "a", Name: "x'y"}, {Key: "a", ClientID: "`z`"}, {Key: "a", ClientSecret: "q\"w"}, {Key: "a", Name: "line\nbreak"}} {
		if o.validate() == nil {
			t.Errorf("опасные настройки приняты: %+v", o)
		}
	}
}

// Скрипт запускается настоящим sh с подставным install.sh: ключи доходят аргументами, ответы — через stdin,
// а служба, оставшаяся жить, не удерживает канал вывода.
func TestSoloistScriptRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("нужен sh")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "install.log")
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("read a; read b; echo \"key=$1 name=$2 cid=$a sec=$b\"; (sleep 20 &); exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := soloistScript(dir, log, SoloistOpts{Key: "spak_abc", Name: "Моя Станция", ClientID: "cid", ClientSecret: "sec"})
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ := os.ReadFile(log)
	if !strings.Contains(string(got), "key=spak_abc name=Моя Станция cid=cid sec=sec") {
		t.Errorf("журнал установщика: %q", got)
	}
}
