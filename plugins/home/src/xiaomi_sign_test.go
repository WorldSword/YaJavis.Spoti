package main

import (
	"encoding/base64"
	"testing"
)

// сверено с эталоном (Xiaomi-cloud-tokens-extractor) на тех же входных данных
func TestXiaomiSignature(t *testing.T) {
	ss := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	nonce := base64.StdEncoding.EncodeToString([]byte("nonce-123456"))
	sn, err := xiaomiSignedNonce(ss, nonce)
	if err != nil {
		t.Fatal(err)
	}
	data := `{"fg": true}`
	hash := xiaomiSign("POST", "/v2/homeroom/gethome", []string{"data"}, []string{data}, sn)
	enc, _ := xiaomiRC4(sn, []byte(data))
	t.Logf("sn=%s hash=%s enc=%s", sn, hash, base64.StdEncoding.EncodeToString(enc))
	back, _ := xiaomiRC4(sn, enc)
	if string(back) != data {
		t.Fatal("RC4 не обратим")
	}
}
