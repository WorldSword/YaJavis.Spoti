package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Голосовой поиск Soloist работает только с Client ID и Secret: без них говорим об этом прямо, а не «exit status 1».
func TestPlayWithoutSearchKeysExplains(t *testing.T) {
	root := fakeSoloistRoot(t)
	if err := os.Remove(filepath.Join(root, "spotify_api.conf")); err != nil {
		t.Fatal(err)
	}
	var calls []spotifyCall
	sp := newSpotifyAt(root, fakeSpotifyExec(&calls))
	err := sp.Play(context.Background(), "Numb")
	if err == nil || !strings.Contains(err.Error(), "Client ID") {
		t.Errorf("ждали подсказку про Client ID и Secret, получили %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("поиск запущен без ключей: %+v", calls)
	}
}

// Вывод скрипта Soloist попадает в сообщение об ошибке, иначе из «exit status 3» ничего не понять.
func TestControlErrorIncludesOutput(t *testing.T) {
	sp := newSpotifyAt(fakeSoloistRoot(t), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("something\n[search] Token request failed\n"), errors.New("exit status 3")
	})
	err := sp.Play(context.Background(), "Numb")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "Token request failed") {
		t.Errorf("в ошибке нет вывода скрипта: %v", err)
	}
}

// Управление (пауза, дальше) не требует ключей поиска.
func TestControlWithoutSearchKeysStillWorks(t *testing.T) {
	root := fakeSoloistRoot(t)
	_ = os.Remove(filepath.Join(root, "spotify_api.conf"))
	var calls []spotifyCall
	sp := newSpotifyAt(root, fakeSpotifyExec(&calls))
	if err := sp.Control(context.Background(), "pause"); err != nil || len(calls) != 1 {
		t.Errorf("пауза без ключей поиска: err=%v calls=%v", err, calls)
	}
}
