package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Diagnostics and local provisioning never print credentials.
func homeAssistantCLI(configure bool) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("укажи файл настроек")
	}
	store, err := OpenStore(os.Args[2])
	if err != nil {
		return err
	}
	cfg := store.Get().HomeAssistant
	if configure {
		if len(os.Args) < 4 {
			return fmt.Errorf("укажи файл подключения Home Assistant")
		}
		b, err := os.ReadFile(os.Args[3])
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return err
		}
	}
	proxy := probeProxy(store, "homeassistant")
	defer proxy.Close()
	h := &HomeAssistant{host: proxy.providers, config: func() HASettings { return cfg }, client: DirectClient}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entities, err := h.catalog(ctx, true)
	if err != nil {
		return err
	}
	if configure {
		if len(cfg.Rooms) == 0 {
			if rooms, e := h.discoverRooms(ctx); e == nil {
				cfg.Rooms = rooms
			}
		}
		if err := store.Update(func(s *Settings) error {
			was := s.HomeConnected("homeassistant")
			s.HomeAssistant = cfg
			s.homeJustConnected("homeassistant", was)
			return nil
		}); err != nil {
			return err
		}
	}
	fmt.Printf("Home Assistant: подключён; сущностей: %d\n", len(entities))
	for _, e := range entities {
		if len(e.ID) >= 7 && e.ID[:7] == "vacuum." {
			fmt.Printf("Пылесос: %s — %s (%s)\n", e.ID, e.name(), e.State)
		}
	}
	if !configure && len(os.Args) > 3 {
		args := os.Args[3]
		if len(args) > 0 && args[0] == '@' {
			b, err := os.ReadFile(args[1:])
			if err != nil {
				return err
			}
			args = string(b)
		}
		fmt.Println(h.control(ctx, args))
	}
	return nil
}
