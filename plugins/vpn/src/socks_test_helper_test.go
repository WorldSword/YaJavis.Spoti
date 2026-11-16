package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
)

func itoa(p uint16) string { return strconv.Itoa(int(p)) }

// socksDialForTest — клиент SOCKS5 с паролем (как в ядре), чтобы проверить сервер.
func socksDialForTest(ctx context.Context, proxy, user, pass, addr string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	host, ps, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(ps)
	buf := []byte{5, 1, 2}
	buf = append(buf, 1, byte(len(user)))
	buf = append(buf, user...)
	buf = append(buf, byte(len(pass)))
	buf = append(buf, pass...)
	buf = append(buf, 5, 1, 0, 3, byte(len(host)))
	buf = append(buf, host...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(port))
	if _, err := c.Write(buf); err != nil {
		c.Close()
		return nil, err
	}
	resp := make([]byte, 2+2+10)
	if _, err := io.ReadFull(c, resp); err != nil {
		c.Close()
		return nil, err
	}
	if resp[1] != 2 || resp[3] != 0 || resp[5] != 0 {
		c.Close()
		return nil, errors.New("отказ SOCKS")
	}
	return c, nil
}
