package main

// Маленький SOCKS5-сервер на 127.0.0.1: только CONNECT и только с логином и паролем, которые плагин
// придумал при запуске и отдал ядру. Так через туннель ходит только сам Джарвис, а не любая программа на колонке.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type socksServer struct {
	ln         net.Listener
	user, pass string
	dial       func(ctx context.Context, host string, port uint16) (net.Conn, error)
	mu         sync.Mutex
	conns      map[net.Conn]bool
	closed     bool
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func startSocks(dial func(ctx context.Context, host string, port uint16) (net.Conn, error)) (*socksServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &socksServer{ln: ln, user: randomToken(), pass: randomToken(), dial: dial, conns: map[net.Conn]bool{}}
	go s.serve()
	return s, nil
}

func (s *socksServer) Addr() string { return s.ln.Addr().String() }

func (s *socksServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		s.mu.Lock()
		if s.closed || len(s.conns) >= 64 {
			s.mu.Unlock()
			c.Close()
			continue
		}
		s.conns[c] = true
		s.mu.Unlock()
		go func() { defer func() { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }(); s.handle(c) }()
	}
}

func (s *socksServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.ln.Close()
	for c := range s.conns {
		c.Close()
	}
}

// Коды ответа SOCKS5.
const (
	repOK          = 0
	repFail        = 1
	repUnreachable = 4
	repCmd         = 7
	repAddr        = 8
)

func (s *socksServer) handle(c net.Conn) {
	ctx, cancel := context.WithCancel(context.Background()) // соединение через туннель живёт, пока жив этот контекст
	defer cancel()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	host, port, ok := s.handshake(c)
	if !ok {
		return
	}
	timer := time.AfterFunc(15*time.Second, cancel)
	remote, err := s.dial(ctx, host, port)
	timer.Stop()
	if err != nil {
		reply(c, repUnreachable)
		return
	}
	defer remote.Close()
	if reply(c, repOK) != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	var once sync.Once
	stop := func() { once.Do(func() { c.Close(); remote.Close() }) }
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(remote, c)
		stop()
		close(done)
	}()
	_, _ = io.Copy(c, remote)
	stop()
	<-done
}

// handshake — приветствие, вход по паролю и запрос CONNECT; вернуть адрес назначения.
func (s *socksServer) handshake(c net.Conn) (string, uint16, bool) {
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 || hdr[1] == 0 {
		return "", 0, false
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", 0, false
	}
	hasPass := false
	for _, m := range methods {
		hasPass = hasPass || m == 2
	}
	if !hasPass {
		_, _ = c.Write([]byte{5, 0xff})
		return "", 0, false
	}
	if _, err := c.Write([]byte{5, 2}); err != nil {
		return "", 0, false
	}
	// RFC 1929: ver=1, ulen, user, plen, pass
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 1 {
		return "", 0, false
	}
	user := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, user); err != nil {
		return "", 0, false
	}
	var plen [1]byte
	if _, err := io.ReadFull(c, plen[:]); err != nil {
		return "", 0, false
	}
	pass := make([]byte, plen[0])
	if _, err := io.ReadFull(c, pass); err != nil {
		return "", 0, false
	}
	good := subtle.ConstantTimeCompare(user, []byte(s.user)) & subtle.ConstantTimeCompare(pass, []byte(s.pass))
	if good != 1 {
		_, _ = c.Write([]byte{1, 1})
		return "", 0, false
	}
	if _, err := c.Write([]byte{1, 0}); err != nil {
		return "", 0, false
	}
	var req [4]byte // ver, cmd, rsv, atyp
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return "", 0, false
	}
	var host string
	switch req[3] {
	case 1, 4:
		ip := make([]byte, map[byte]int{1: 4, 4: 16}[req[3]])
		if _, err := io.ReadFull(c, ip); err != nil {
			return "", 0, false
		}
		host = net.IP(ip).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil || n[0] == 0 {
			return "", 0, false
		}
		name := make([]byte, n[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return "", 0, false
		}
		host = string(name)
	default:
		reply(c, repAddr)
		return "", 0, false
	}
	var pb [2]byte
	if _, err := io.ReadFull(c, pb[:]); err != nil {
		return "", 0, false
	}
	if req[1] != 1 { // только CONNECT
		reply(c, repCmd)
		return "", 0, false
	}
	port := binary.BigEndian.Uint16(pb[:])
	if port == 0 {
		reply(c, repFail)
		return "", 0, false
	}
	return host, port, true
}

func reply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
