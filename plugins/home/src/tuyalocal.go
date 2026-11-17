package main

// Устройства Tuya по домашней сети: сами раз в несколько секунд рассказывают о себе (UDP 6666/6667/7000) —
// так узнаём их адрес и версию протокола. Команда — короткое TCP-соединение на порт 6668, шифрование
// локальным ключом устройства (3.3 — AES-ECB + CRC, 3.4 — ключ сессии + HMAC, 3.5 — ключ сессии + AES-GCM).
// Формат пакетов описан в открытом tinytuya (MIT), код свой.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const (
	tuyaPrefix55 = 0x000055AA
	tuyaSuffix55 = 0x0000AA55
	tuyaPrefix66 = 0x00006699
	tuyaSuffix66 = 0x00009966

	tuyaCmdNegStart  = 3
	tuyaCmdNegResp   = 4
	tuyaCmdNegFinish = 5
	tuyaCmdControl   = 7
	tuyaCmdControl34 = 13
)

var tuyaUDPKey = func() []byte { s := md5.Sum([]byte("yGAdlopoPVldABfn")); return s[:] }()

type tuyaPeer struct {
	IP      string
	Version string
	Seen    time.Time
}

// TuyaLAN — кто из устройств Tuya виден в домашней сети.
type TuyaLAN struct {
	mu    sync.Mutex
	peers map[string]tuyaPeer // id устройства → адрес и версия
	seq   uint32
	port  int // для проверки: порт устройства вместо 6668
}

func (l *TuyaLAN) Known(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.peers[id]
	return ok && time.Since(p.Seen) < 10*time.Minute
}

func (l *TuyaLAN) peer(id string) (tuyaPeer, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.peers[id]
	return p, ok && time.Since(p.Seen) < 10*time.Minute
}

// Listen — слушать объявления устройств, пока want() (вход в Smart Life выполнен).
func (l *TuyaLAN) Listen(want func() bool) {
	for _, port := range []int{6666, 6667, 7000} {
		go func(port int) {
			for {
				if !want() {
					time.Sleep(30 * time.Second)
					continue
				}
				c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
				if err != nil {
					log.Printf("Smart Life: порт %d занят (%v) — поиск устройств без него", port, err)
					time.Sleep(10 * time.Minute)
					continue
				}
				buf := make([]byte, 4096)
				for want() {
					_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
					n, from, err := c.ReadFromUDP(buf)
					if err != nil {
						continue
					}
					l.announce(buf[:n], from)
				}
				c.Close()
			}
		}(port)
	}
}

func (l *TuyaLAN) announce(pkt []byte, from *net.UDPAddr) {
	plain := tuyaUDPDecode(pkt)
	var info struct {
		IP      string `json:"ip"`
		GwID    string `json:"gwId"`
		Version string `json:"version"`
	}
	if plain == nil || json.Unmarshal(plain, &info) != nil || info.GwID == "" {
		return
	}
	if info.IP == "" && from != nil {
		info.IP = from.IP.String()
	}
	if !tuyaIsLAN(info.IP) {
		return
	}
	l.mu.Lock()
	if l.peers == nil {
		l.peers = map[string]tuyaPeer{}
	}
	old, had := l.peers[info.GwID]
	l.peers[info.GwID] = tuyaPeer{IP: info.IP, Version: info.Version, Seen: time.Now()}
	l.mu.Unlock()
	if !had || old.IP != info.IP {
		log.Printf("Smart Life: в сети устройство %s… (%s, протокол %s)", info.GwID[:min(6, len(info.GwID))], info.IP, info.Version)
	}
}

// tuyaUDPDecode — содержимое объявления (JSON) из пакета любого формата.
func tuyaUDPDecode(pkt []byte) []byte {
	if len(pkt) < 20 {
		return nil
	}
	switch binary.BigEndian.Uint32(pkt) {
	case tuyaPrefix55:
		n := int(binary.BigEndian.Uint32(pkt[12:16]))
		if 16+n > len(pkt) || n < 12 {
			return nil
		}
		body := pkt[20 : 16+n-8] // без кода ответа, CRC и хвоста
		if len(body) > 0 && body[0] == '{' {
			return body
		}
		return aesECBDecrypt(tuyaUDPKey, body)
	case tuyaPrefix66:
		raw, err := tuya66Open(pkt, tuyaUDPKey)
		if err != nil {
			return nil
		}
		if len(raw) > 4 && raw[0] != '{' && raw[4] == '{' {
			raw = raw[4:]
		}
		return bytes.TrimRight(raw, "\x00")
	}
	return nil
}

// ---------------------------------------------------------------- шифрование

func pkcs7(b []byte) []byte {
	n := 16 - len(b)%16
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func aesECBEncrypt(key, plain []byte, pad bool) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	if pad {
		plain = pkcs7(plain)
	}
	out := make([]byte, len(plain))
	for i := 0; i+16 <= len(plain); i += 16 {
		block.Encrypt(out[i:i+16], plain[i:i+16])
	}
	return out
}

func aesECBDecrypt(key, enc []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil || len(enc) == 0 || len(enc)%16 != 0 {
		return nil
	}
	out := make([]byte, len(enc))
	for i := 0; i < len(enc); i += 16 {
		block.Decrypt(out[i:i+16], enc[i:i+16])
	}
	if n := int(out[len(out)-1]); n > 0 && n <= 16 && n <= len(out) {
		out = out[:len(out)-n]
	}
	return out
}

// ---------------------------------------------------------------- пакеты 55AA (3.3, 3.4)

func tuya55Pack(seq, cmd uint32, payload, hmacKey []byte) []byte {
	endLen := 8
	if hmacKey != nil {
		endLen = 36
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, []uint32{tuyaPrefix55, seq, cmd, uint32(len(payload) + endLen)})
	b.Write(payload)
	if hmacKey != nil {
		m := hmac.New(sha256.New, hmacKey)
		m.Write(b.Bytes())
		b.Write(m.Sum(nil))
	} else {
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	}
	_ = binary.Write(&b, binary.BigEndian, uint32(tuyaSuffix55))
	return b.Bytes()
}

type tuyaFrame struct {
	cmd     uint32
	retcode uint32
	payload []byte
}

// tuyaRead — следующий пакет из соединения (оба формата).
func tuyaRead(c net.Conn, key []byte, hmacKey bool) (tuyaFrame, error) {
	head := make([]byte, 16)
	if _, err := io.ReadFull(c, head); err != nil {
		return tuyaFrame{}, err
	}
	switch binary.BigEndian.Uint32(head) {
	case tuyaPrefix55:
		cmd, n := binary.BigEndian.Uint32(head[8:12]), int(binary.BigEndian.Uint32(head[12:16]))
		if n < 12 || n > 1<<16 {
			return tuyaFrame{}, errors.New("неверный пакет")
		}
		rest := make([]byte, n)
		if _, err := io.ReadFull(c, rest); err != nil {
			return tuyaFrame{}, err
		}
		end := 8
		if hmacKey {
			end = 36
		}
		if n < 4+end {
			return tuyaFrame{}, errors.New("короткий пакет")
		}
		f := tuyaFrame{cmd: cmd, retcode: binary.BigEndian.Uint32(rest[:4]), payload: rest[4 : n-end]}
		if hmacKey {
			m := hmac.New(sha256.New, key)
			m.Write(head)
			m.Write(rest[:n-end])
			if !hmac.Equal(m.Sum(nil), rest[n-end:n-4]) {
				return tuyaFrame{}, errors.New("неверная подпись пакета — не тот ключ")
			}
		}
		return f, nil
	case tuyaPrefix66:
		more := make([]byte, 2) // заголовок 6699 на 2 байта длиннее
		if _, err := io.ReadFull(c, more); err != nil {
			return tuyaFrame{}, err
		}
		head = append(head, more...)
		n := int(binary.BigEndian.Uint32(head[14:18]))
		if n < 28 || n > 1<<16 {
			return tuyaFrame{}, errors.New("неверный пакет")
		}
		rest := make([]byte, n+4)
		if _, err := io.ReadFull(c, rest); err != nil {
			return tuyaFrame{}, err
		}
		raw, err := tuya66Open(append(head, rest...), key)
		if err != nil {
			return tuyaFrame{}, err
		}
		f := tuyaFrame{cmd: binary.BigEndian.Uint32(head[10:14])}
		if len(raw) >= 4 && (len(raw) == 4 || raw[0] != '{') {
			f.retcode, raw = binary.BigEndian.Uint32(raw[:4]), raw[4:]
		}
		f.payload = raw
		return f, nil
	}
	return tuyaFrame{}, errors.New("неизвестный формат пакета")
}

// ---------------------------------------------------------------- пакеты 6699 (3.5, AES-GCM)

func tuya66Pack(seq, cmd uint32, raw, key []byte) []byte {
	head := make([]byte, 18)
	binary.BigEndian.PutUint32(head[0:], tuyaPrefix66)
	binary.BigEndian.PutUint32(head[6:], seq)
	binary.BigEndian.PutUint32(head[10:], cmd)
	binary.BigEndian.PutUint32(head[14:], uint32(12+len(raw)+16))
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	iv := make([]byte, 12)
	_, _ = rand.Read(iv)
	out := append(head, iv...)
	out = g.Seal(out, iv, raw, head[4:])
	return binary.BigEndian.AppendUint32(out, tuyaSuffix66)
}

func tuya66Open(pkt, key []byte) ([]byte, error) {
	if len(pkt) < 18+12+16+4 {
		return nil, errors.New("короткий пакет")
	}
	n := int(binary.BigEndian.Uint32(pkt[14:18]))
	if 18+n+4 > len(pkt) || n < 28 {
		return nil, errors.New("неверная длина")
	}
	body := pkt[18 : 18+n]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, body[:12], body[12:], pkt[4:18])
}

// ---------------------------------------------------------------- команда устройству

// Control — выставить dps («1»: true …) на устройстве по домашней сети.
func (l *TuyaLAN) Control(ctx context.Context, id, localKey string, dps map[string]any) error {
	p, ok := l.peer(id)
	if !ok {
		return errors.New("устройство не видно в домашней сети")
	}
	key := []byte(localKey)
	if len(key) != 16 {
		return errors.New("нет локального ключа")
	}
	d := net.Dialer{}
	port := 6668
	if l.port != 0 {
		port = l.port
	}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.IP, fmt.Sprint(port)))
	if err != nil {
		return err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	l.mu.Lock()
	l.seq++
	seq := l.seq * 16
	l.mu.Unlock()
	ver := p.Version
	switch ver {
	case "3.3", "3.2", "3.1", "":
		js, _ := json.Marshal(map[string]any{"devId": id, "uid": id, "t": fmt.Sprint(time.Now().Unix()), "dps": dps})
		payload := append([]byte("3.3\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), aesECBEncrypt(key, js, true)...)
		if _, err := c.Write(tuya55Pack(seq, tuyaCmdControl, payload, nil)); err != nil {
			return err
		}
		f, err := tuyaRead(c, key, false)
		if err != nil {
			return err
		}
		if f.retcode != 0 {
			return fmt.Errorf("устройство ответило кодом %d", f.retcode)
		}
		return nil
	case "3.4", "3.5":
		v35 := ver == "3.5"
		sess, err := tuyaNegotiate(c, key, seq, v35)
		if err != nil {
			return fmt.Errorf("ключ сессии: %w", err)
		}
		js, _ := json.Marshal(map[string]any{"protocol": 5, "t": time.Now().Unix(), "data": map[string]any{"dps": dps}})
		raw := append([]byte(ver+"\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), js...)
		var pkt []byte
		if v35 {
			pkt = tuya66Pack(seq+3, tuyaCmdControl34, raw, sess)
		} else {
			pkt = tuya55Pack(seq+3, tuyaCmdControl34, aesECBEncrypt(sess, raw, true), sess)
		}
		if _, err := c.Write(pkt); err != nil {
			return err
		}
		f, err := tuyaRead(c, sess, !v35)
		if err != nil {
			return err
		}
		if f.retcode != 0 {
			return fmt.Errorf("устройство ответило кодом %d", f.retcode)
		}
		return nil
	}
	return fmt.Errorf("протокол %s не поддерживается", ver)
}

// tuyaNegotiate — ключ сессии 3.4/3.5: обмен случайными числами, подписанными локальным ключом.
func tuyaNegotiate(c net.Conn, key []byte, seq uint32, v35 bool) ([]byte, error) {
	local := make([]byte, 16)
	_, _ = rand.Read(local)
	send := func(s, cmd uint32, raw []byte) error {
		var pkt []byte
		if v35 {
			pkt = tuya66Pack(s, cmd, raw, key)
		} else {
			pkt = tuya55Pack(s, cmd, aesECBEncrypt(key, raw, true), key)
		}
		_, err := c.Write(pkt)
		return err
	}
	if err := send(seq+1, tuyaCmdNegStart, local); err != nil {
		return nil, err
	}
	f, err := tuyaRead(c, key, !v35)
	if err != nil {
		return nil, err
	}
	if f.cmd != tuyaCmdNegResp {
		return nil, fmt.Errorf("ответ %d вместо 4", f.cmd)
	}
	body := f.payload
	if !v35 {
		body = aesECBDecrypt(key, body)
	}
	if len(body) < 48 {
		return nil, errors.New("короткий ответ")
	}
	remote := body[:16]
	m := hmac.New(sha256.New, key)
	m.Write(local)
	if !hmac.Equal(m.Sum(nil), body[16:48]) {
		return nil, errors.New("устройство не подтвердило ключ — проверьте, что ключ свежий («Обновить устройства»)")
	}
	m = hmac.New(sha256.New, key)
	m.Write(remote)
	if err := send(seq+2, tuyaCmdNegFinish, m.Sum(nil)); err != nil {
		return nil, err
	}
	x := make([]byte, 16)
	for i := range x {
		x[i] = local[i] ^ remote[i]
	}
	if !v35 {
		return aesECBEncrypt(key, x, false), nil
	}
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	return g.Seal(nil, local[:12], x, nil)[:16], nil
}

// tuyaIsLAN — адрес из домашней сети (объявления из интернета не принимаем).
func tuyaIsLAN(ip string) bool {
	a := net.ParseIP(ip)
	return a != nil && (a.IsPrivate() || a.IsLinkLocalUnicast())
}
