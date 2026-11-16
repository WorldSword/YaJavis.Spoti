package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dop251/goja"
)

func TestPlayerDrainHelper(t *testing.T) {
	if os.Getenv("YAJAVIS_PLAYER_TEST_CHILD") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestPlayerConcurrentFinishStopWrite(t *testing.T) {
	for n := 0; n < 8; n++ {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestPlayerDrainHelper$")
		cmd.Env = append(os.Environ(), "YAJAVIS_PLAYER_TEST_CHILD=1")
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		p := &Player{cmd: cmd, in: in, queue: make(chan []byte, 4), done: make(chan struct{}), exited: make(chan struct{}), lim: newLimiter(16000)}
		go p.pump(in, p.queue, p.done)
		exited := p.exited
		go func() { _ = cmd.Wait(); close(exited) }()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				for j := 0; j < 30; j++ {
					_ = p.Write(make([]int16, 64))
				}
			})
		}
		wg.Go(func() { p.Finish(time.Second) })
		wg.Go(func() { p.Finish(time.Second) })
		wg.Go(func() { p.Stop(false) })
		wg.Wait()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("player child did not exit")
		}
	}
}

func TestSettingsSnapshotOwnsNestedValues(t *testing.T) {
	s := defaultSettings()
	s.VPN = map[string]bool{"gemini": true}
	s.HomeUse = map[string]bool{"xiaomi": true}
	s.GeminiKeys = []string{"test"}
	s.ElevenKeys = []ElevenKey{{Key: "test"}}
	s.HomeAssistant.Rooms = []HARoomBinding{{Name: "room", Aliases: []string{"alias"}}}
	s.Profiles[0].Led = &LedSettings{Listening: "#ffffff"}
	st := &Store{s: s}
	c := st.Get()
	c.VPN["gemini"] = false
	c.HomeUse["xiaomi"] = false
	c.GeminiKeys[0] = "changed"
	c.ElevenKeys[0].Key = "changed"
	c.HomeAssistant.Rooms[0].Name = "changed"
	c.HomeAssistant.Rooms[0].Aliases[0] = "changed"
	c.Profiles[0].Led.Listening = "changed"
	got := st.Get()
	if got.Profiles[0].Led.Listening != "#ffffff" {
		t.Fatal("profile LED snapshot is shared")
	}
	if !got.VPN["gemini"] || !got.HomeUse["xiaomi"] || got.GeminiKeys[0] != "test" || got.ElevenKeys[0].Key != "test" || got.HomeAssistant.Rooms[0].Name != "room" || got.HomeAssistant.Rooms[0].Aliases[0] != "alias" {
		t.Fatal("snapshot mutated stored values")
	}
}

func TestSTTFeedDoesNotWaitForNetworkAndOwnsAudio(t *testing.T) {
	s := &STT{ws: &websocket.Conn{}, active: true, gen: 7, feed: make(chan sttChunk, 2)}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	done := make(chan struct{})
	pcm := []int16{1, 2, 3}
	go func() { s.Feed(pcm); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Feed waited on network writer")
	}
	pcm[0] = 99
	c := <-s.feed
	if c.pcm[0] != 1 || c.gen != 7 {
		t.Fatal("queued audio changed with input buffer")
	}
	for i := 0; i < 20; i++ {
		s.Feed(pcm)
	}
	if len(s.feed) != cap(s.feed) {
		t.Fatal("queue should stay bounded")
	}
}

func TestServiceHTTPPoolReuseAndRouteChange(t *testing.T) {
	p := &Proxy{}
	first := p.servicePool("gemini")
	if p.servicePool("gemini") != first {
		t.Fatal("pool is recreated")
	}
	// A changed Xray instance must retire connections from the previous route.
	p.link = "vless://test"
	p.tun = &vpnTunnel{Socks: "127.0.0.1:1", User: "test", Pass: "test"}
	second := p.servicePool("gemini")
	if second == first {
		t.Fatal("old route reused")
	}
	p.use = func(string) bool { return false }
	if p.servicePool("gemini") == second {
		t.Fatal("VPN pool reused for direct traffic")
	}
	p.link = ""
	p.tun = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer server.Close()
	client := p.ClientFor("gemini", time.Second)
	for i := 0; i < 2; i++ {
		r, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	p.servicePool("gemini").CloseIdleConnections()
}

type blockedExportCloud struct {
	*fakeCloud
	started chan string
	release chan struct{}
}

func (c *blockedExportCloud) get(ctx context.Context, id string) ([]byte, error) {
	c.started <- id
	if id == "first" {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []byte("audio"), nil
}

func TestTrainingExportBoundsPrefetch(t *testing.T) {
	cloud := &blockedExportCloud{newFakeCloud(), make(chan string, 100), make(chan struct{})}
	tr := &Trainer{dataDir: t.TempDir(), cloud: cloud}
	tr.rm.idx = &remoteIndex{}
	for i := 0; i < 100; i++ {
		id := fmt.Sprint(i)
		if i == 0 {
			id = "first"
		}
		tr.rm.idx.Clips = append(tr.rm.idx.Clips, remoteClip{ID: id, Name: id + ".wav", Kind: "pos", Time: time.Unix(int64(100-i), 0)})
	}
	w := &Web{engine: &Engine{train: tr}, dataDir: tr.dataDir}
	out := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); w.downloadTrain(out, httptest.NewRequest("GET", "/", nil)) }()
	for i := 0; i < 2; i++ {
		select {
		case <-cloud.started:
		case <-time.After(time.Second):
			close(cloud.release)
			t.Fatal("download did not start")
		}
	}
	select {
	case id := <-cloud.started:
		close(cloud.release)
		<-done
		t.Fatalf("prefetch ran ahead while first clip blocked: %s", id)
	case <-time.After(100 * time.Millisecond):
	}
	close(cloud.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("export did not finish")
	}
	z, err := zip.NewReader(bytes.NewReader(out.Body.Bytes()), int64(out.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range z.File {
		if len(f.Name) > 4 && f.Name[len(f.Name)-4:] == ".wav" {
			n++
		}
	}
	if n != 100 {
		t.Fatalf("exported %d clips", n)
	}
}

func TestYouTubeWarmDoesNotExtendIdle(t *testing.T) {
	vm := goja.New()
	_ = vm.Set("__player", "test")
	used := time.Now().Add(-time.Hour)
	j := &ytJS{id: "test", idAt: time.Now(), vm: vm, used: used}
	if _, err := j.prepareWithUse(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	if j.used != used {
		t.Fatal("warm check counted as real use")
	}
	j.used = time.Now().Add(-ytJSIdle - time.Hour)
	j.vm = nil
	if _, err := j.prepareWithUse(context.Background(), nil, false); err != nil || j.vm != nil {
		t.Fatal("warm restarted expired VM")
	}
}
