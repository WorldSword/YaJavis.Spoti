package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

// Account logout remains available even when its optional provider is stopped.
func forgetHomeAccount(store *Store, service string) error {
	if err := store.Update(func(s *Settings) error {
		switch service {
		case "smartlife":
			s.Tuya = TuyaAuth{}
		case "xiaomi":
			s.Xiaomi = XiaomiAuth{}
		default:
			return errors.New("неизвестное подключение")
		}
		return nil
	}); err != nil {
		return err
	}
	name := map[string]string{"smartlife": "tuya-devices.json", "xiaomi": "xiaomi-devices.json"}[service]
	dir := filepath.Dir(store.path)
	for _, path := range []string{filepath.Join(dir, name), filepath.Join(dir, "plugins", "home", "data", name)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Explicit per-provider mapping: never hand all of settings.json to a plugin.
func coreServices(store *Store, proxy *Proxy, dataDir string) func(string, string, json.RawMessage) (any, error) {
	return func(id, method string, params json.RawMessage) (any, error) {
		service := map[string]string{"homeassistant": "homeassistant", "xiaomi": "xiaomi", "smartlife": "smartlife", "youtube": "youtube", "chatgpt": "chatgpt"}[id]
		if id == "home" {
			var scope struct {
				Service string `json:"service"`
			}
			_ = json.Unmarshal(params, &scope)
			if scope.Service == "homeassistant" || scope.Service == "xiaomi" || scope.Service == "smartlife" || scope.Service == "yandex" {
				service = scope.Service
			}
		}
		if service == "" {
			return nil, errors.New("у плагина нет доступа к этому сервису ядра")
		}
		s := store.Get()
		switch method {
		case "network":
			if service == "homeassistant" {
				return map[string]any{}, nil
			}
			_, tunnel, why := proxy.route(service)
			if why != "" {
				return map[string]any{"error": why}, nil
			}
			if tunnel == nil {
				return map[string]any{}, nil
			}
			return map[string]any{"socks": tunnel.Socks, "user": tunnel.User, "pass": tunnel.Pass}, nil
		case "config.get":
			var auth any
			switch service {
			case "homeassistant":
				auth = s.HomeAssistant
			case "xiaomi":
				auth = s.Xiaomi
			case "smartlife":
				auth = s.Tuya
			default:
				return nil, errors.New("сервис ещё не поддерживает перенос настроек")
			}
			return map[string]any{"auth": auth, "speaker_room": s.SpeakerRoom, "enabled": s.UseHome(service)}, nil
		case "config.set":
			var change struct {
				Before    json.RawMessage `json:"before"`
				After     json.RawMessage `json:"after"`
				Connected bool            `json:"connected"`
			}
			if json.Unmarshal(params, &change) != nil || len(change.Before) == 0 || len(change.After) == 0 {
				return nil, errors.New("неверное обновление настроек")
			}
			return true, store.Update(func(st *Settings) error {
				was := st.HomeConnected(service)
				var old any
				switch service {
				case "homeassistant":
					old = st.HomeAssistant
				case "xiaomi":
					old = st.Xiaomi
				case "smartlife":
					old = st.Tuya
				default:
					return errors.New("настройки сервиса недоступны")
				}
				b, _ := json.Marshal(old)
				var current, expected any
				_ = json.Unmarshal(b, &current)
				if json.Unmarshal(change.Before, &expected) != nil || !reflect.DeepEqual(current, expected) {
					return errors.New("settings changed; retry")
				}
				var err error
				switch service {
				case "homeassistant":
					var v HASettings
					err = json.Unmarshal(change.After, &v)
					if err == nil {
						st.HomeAssistant = v
					}
				case "xiaomi":
					var v XiaomiAuth
					err = json.Unmarshal(change.After, &v)
					if err == nil {
						st.Xiaomi = v
					}
				case "smartlife":
					var v TuyaAuth
					err = json.Unmarshal(change.After, &v)
					if err == nil {
						st.Tuya = v
					}
				default:
					return errors.New("настройки сервиса недоступны")
				}
				if err == nil && change.Connected {
					st.homeJustConnected(service, was)
				}
				return err
			})
		case "legacy":
			name := map[string]string{"xiaomi": "xiaomi-devices.json", "smartlife": "tuya-devices.json"}[service]
			if name == "" {
				return nil, errors.New("у сервиса нет старого кеша")
			}
			b, err := os.ReadFile(filepath.Join(dataDir, name))
			if os.IsNotExist(err) {
				return []byte(nil), nil
			}
			if len(b) > 4<<20 {
				return nil, errors.New("старый кеш слишком большой")
			}
			return b, err
		}
		return nil, errors.New("неизвестный сервис ядра")
	}
}
