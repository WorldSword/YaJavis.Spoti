package main

import (
	"context"
	"encoding/json"
	"github.com/newfpv/YaJavis/pluginsdk"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Settings struct {
	Tuya          TuyaAuth
	Xiaomi        XiaomiAuth
	SpeakerRoom   string
	justConnected bool
}
type Store struct {
	sdk     *pluginsdk.Plugin
	service string
	mu      sync.Mutex
}

func (s *Store) read() (Settings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out struct {
		Auth json.RawMessage `json:"auth"`
		Room string          `json:"speaker_room"`
	}
	err := s.sdk.Core(ctx, "config.get", map[string]any{"service": s.service}, &out)
	st := Settings{SpeakerRoom: out.Room}
	if err != nil {
		return st, err
	}
	if s.service == "smartlife" {
		err = json.Unmarshal(out.Auth, &st.Tuya)
	} else {
		err = json.Unmarshal(out.Auth, &st.Xiaomi)
	}
	return st, err
}
func (s *Store) Get() Settings { out, _ := s.read(); return out }
func (s *Store) auth(st Settings) any {
	if s.service == "smartlife" {
		return st.Tuya
	}
	return st.Xiaomi
}
func (s *Store) Update(fn func(*Settings) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		st, e := s.read()
		if e != nil {
			return e
		}
		before := s.auth(st)
		if e = fn(&st); e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = s.sdk.Core(ctx, "config.set", map[string]any{"service": s.service, "before": before, "after": s.auth(st), "connected": st.justConnected}, nil)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "settings changed; retry") {
			return err
		}
	}
	return err
}
func migrate(p *pluginsdk.Plugin, in pluginsdk.Init, service string) {
	name := map[string]string{"smartlife": "tuya-devices.json", "xiaomi": "xiaomi-devices.json"}[service]
	path := filepath.Join(in.Data, name)
	marker := path + ".migrated"
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		_ = os.WriteFile(marker, []byte("1"), 0600)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var b []byte
	if p.Core(ctx, "legacy", map[string]any{"service": service}, &b) == nil {
		if len(b) > 0 {
			if !json.Valid(b) {
				return
			}
			if os.WriteFile(path, b, 0600) != nil {
				return
			}
		}
		_ = os.WriteFile(marker, []byte("1"), 0600)
	}
}
