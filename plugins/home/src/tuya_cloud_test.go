package main

import (
	"os"
	"strings"
	"testing"
)

// векторы посчитаны кодом tuya-device-sharing-sdk (customerapi.py): TUYA_VEC=файл go test -run TestTuyaCloud
func TestTuyaCloudVectors(t *testing.T) {
	b, err := os.ReadFile(os.Getenv("TUYA_VEC"))
	if err != nil {
		t.Skip("нет векторов")
	}
	f := strings.Fields(string(b))
	hashKey, secret, enc, sign, resp := f[0], f[1], f[2], f[3], f[4]
	rid := "11111111-2222-4333-8444-555555555555"
	if got := string(tuyaSecret(rid, hashKey)); got != secret {
		t.Fatalf("секрет %s, ждали %s", got, secret)
	}
	h := map[string]string{"X-appKey": tuyaClientID, "X-requestId": rid, "X-sid": "", "X-time": "1700000000000", "X-token": "tok"}
	if got := tuyaSign(hashKey, enc, "", h); got != sign {
		t.Fatalf("подпись %s, ждали %s", got, sign)
	}
	plain, err := tuyaDecrypt(resp, []byte(secret))
	if err != nil || string(plain) != `[{"ownerId":7}]` {
		t.Fatalf("расшифровка: %q %v", plain, err)
	}
	mine, _ := tuyaEncrypt([]byte(`{"homeId":"42"}`), []byte(secret))
	if len(mine) != len(enc) {
		t.Fatalf("формат шифра: %d против %d", len(mine), len(enc))
	}
}
