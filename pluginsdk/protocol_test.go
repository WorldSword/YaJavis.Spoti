package pluginsdk

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("JARVIS_SDK_TEST_HELPER") == "1" {
		p := New()
		ready := ""
		p.OnInit(func(Init) { ready = "initialized" })
		p.HandleContext("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			if ready != "initialized" {
				return "not initialized", nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		p.Run()
		return
	}
	os.Exit(m.Run())
}

func TestProtocolDeadlineAndCancel(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), "JARVIS_PLUGIN=1", "JARVIS_SDK_TEST_HELPER=1")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	w := json.NewEncoder(in)
	r := json.NewDecoder(out)
	read := func(id int64) msg {
		for {
			var m msg
			if err := r.Decode(&m); err != nil {
				t.Fatal(err)
			}
			if m.ID == id && m.Method == "" {
				return m
			}
		}
	}
	_ = w.Encode(msg{ID: 1, Method: "init", Params: json.RawMessage(`{}`)})
	if m := read(1); m.Error != "" {
		t.Fatal(m.Error)
	}
	_ = w.Encode(msg{ID: 2, Method: "wait", Deadline: time.Now().Add(50 * time.Millisecond).UnixMilli()})
	if m := read(2); m.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("deadline: %+v", m)
	}
	_ = w.Encode(msg{ID: 3, Method: "wait"})
	_ = w.Encode(msg{Method: "cancel", Params: json.RawMessage(`{"id":3}`)})
	if m := read(3); m.Error != context.Canceled.Error() {
		t.Fatalf("cancel: %+v", m)
	}
}
