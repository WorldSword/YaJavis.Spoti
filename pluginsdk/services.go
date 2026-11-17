package pluginsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Core uses the provider's scoped core services. Credentials stay on the speaker.
func (p *Plugin) Core(ctx context.Context, method string, params, out any) error {
	raw, err := p.Request(ctx, "core", map[string]any{"method": method, "params": params})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type networkRoute struct {
	Socks string `json:"socks"`
	User  string `json:"user"`
	Pass  string `json:"pass"`
	Error string `json:"error"`
}

type serviceTransport struct {
	p       *Plugin
	service string
	mu      sync.Mutex
	key     string
	t       *http.Transport
}

// HTTPClient follows the provider's service VPN switch for every request.
// A failed route lookup never becomes a direct connection.
func (p *Plugin) HTTPClient(timeout time.Duration) *http.Client {
	return p.HTTPClientFor("", timeout)
}

// HTTPClientFor selects one service when several adapters share a process.
func (p *Plugin) HTTPClientFor(service string, timeout time.Duration) *http.Client {
	p.httpMu.Lock()
	if p.httpTransports == nil {
		p.httpTransports = map[string]*serviceTransport{}
	}
	tr := p.httpTransports[service]
	if tr == nil {
		tr = &serviceTransport{p: p, service: service}
		p.httpTransports[service] = tr
	}
	p.httpMu.Unlock()
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (t *serviceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var route networkRoute
	if err := t.p.Core(r.Context(), "network", map[string]any{"service": t.service}, &route); err != nil {
		return nil, err
	}
	if route.Error != "" {
		return nil, errors.New(route.Error)
	}
	key := route.Socks + "|" + route.User + "|" + route.Pass
	t.mu.Lock()
	if t.t == nil || t.key != key {
		if t.t != nil {
			t.t.CloseIdleConnections()
		}
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dial := d.DialContext
		if route.Socks != "" {
			s, err := proxy.SOCKS5("tcp", route.Socks, &proxy.Auth{User: route.User, Password: route.Pass}, d)
			if err != nil {
				t.mu.Unlock()
				return nil, err
			}
			cd, ok := s.(proxy.ContextDialer)
			if !ok {
				t.mu.Unlock()
				return nil, errors.New("SOCKS context unavailable")
			}
			dial = cd.DialContext
		}
		t.t = &http.Transport{DialContext: dial, MaxIdleConns: 8, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
		t.key = key
	}
	tr := t.t
	t.mu.Unlock()
	return tr.RoundTrip(r)
}
