package main

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// С поддельным устройством на Python (логика tinytuya): TUYA_FAKE_PORT=… TUYA_FAKE_VER=3.4 go test -run TestTuyaFake
func TestTuyaFake(t *testing.T) {
	port, _ := strconv.Atoi(os.Getenv("TUYA_FAKE_PORT"))
	if port == 0 {
		t.Skip("нет поддельного устройства")
	}
	l := &TuyaLAN{port: port, peers: map[string]tuyaPeer{"dev1": {IP: "127.0.0.1", Version: os.Getenv("TUYA_FAKE_VER"), Seen: time.Now()}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Control(ctx, "dev1", "0123456789abcdef", map[string]any{"20": true, "22": 500}); err != nil {
		t.Fatal(err)
	}
}

func TestTuyaUDP(t *testing.T) {
	js := []byte(`{"ip":"192.168.1.31","gwId":"bf123456","version":"3.4"}`)
	pkt := tuya55Pack(0, 19, append([]byte{0, 0, 0, 0}, aesECBEncrypt(tuyaUDPKey, js, true)...), nil)
	if got := string(tuyaUDPDecode(pkt)); got != string(js) {
		t.Fatalf("6667: %q", got)
	}
	l := &TuyaLAN{}
	l.announce(pkt, nil)
	if p, ok := l.peer("bf123456"); !ok || p.IP != "192.168.1.31" || p.Version != "3.4" {
		t.Fatalf("устройство не запомнено: %+v", p)
	}
}
