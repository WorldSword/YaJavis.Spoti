package main

// Нативные плагины: отдельная программа под архитектуру колонки. jarvisd запускает её и говорит с ней строками
// JSON через stdin/stdout (одна строка — одно сообщение), stderr идёт в журнал. Упала — перезапуск с паузой,
// пять падений за 10 минут — плагин остаётся выключенным до перезапуска Джарвиса. Протокол — docs/PLUGINS.md;
// для Go есть готовая обёртка pluginsdk.
//
//   ядро → плагин: init {api, version, arch, dir, data, settings, info} → ok;  tool {name, args} → строка;
//                  command {text} → {handled, answer};  settings {settings};  shutdown
//   плагин → ядро: register {tools, commands};  status {text};  log {text};  say {text};  alice {text, silent} → bool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const nativeLineMax = 4 << 20

type pluginMsg struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type nativePlugin struct {
	host *PluginHost
	p    *Plugin
	bin  string

	mu       sync.Mutex
	settings map[string]any
	cmd      *exec.Cmd
	in       io.WriteCloser
	wmu      sync.Mutex
	nextID   int64
	pending  map[int64]chan pluginMsg
	tools    []any
	commands bool
	status   string
	fails    []time.Time
	stopped  bool
	done     chan struct{} // закрывается, когда текущий процесс завершился
}

func startNativePlugin(h *PluginHost, p *Plugin, settings map[string]any) (pluginRuntime, error) {
	bin := filepath.Join(p.Dir, strings.ReplaceAll(p.Manifest.Main, "{arch}", pluginArch()))
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("нет программы плагина для этой колонки (%s)", filepath.Base(bin))
	}
	n := &nativePlugin{host: h, p: p, bin: bin, settings: settings, pending: map[int64]chan pluginMsg{}}
	if err := n.spawn(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *nativePlugin) spawn() error {
	data := filepath.Join(n.p.Dir, "data")
	_ = os.MkdirAll(data, 0o700)
	cmd := exec.Command(n.bin)
	cmd.Dir = n.p.Dir
	cmd.Env = append(os.Environ(), "JARVIS_PLUGIN=1", "JARVIS_PLUGIN_ID="+n.p.Manifest.ID, "JARVIS_PLUGIN_DIR="+n.p.Dir,
		"JARVIS_PLUGIN_DATA="+data, fmt.Sprintf("JARVIS_PLUGIN_API=%d", pluginAPI))
	cmd.SysProcAttr = pluginProcAttr()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errp, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	n.mu.Lock()
	n.cmd, n.in, n.done = cmd, in, done
	n.tools, n.commands = nil, false
	settings := n.settings
	n.mu.Unlock()
	go n.logStderr(errp)
	go n.read(out, done)
	go func() {
		err := cmd.Wait()
		close(done)
		n.exited(err)
	}()
	info := map[string]any{}
	if n.host.hooks.Info != nil {
		info = n.host.hooks.Info()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := n.request(ctx, "init", map[string]any{"api": pluginAPI, "version": version, "arch": pluginArch(),
		"dir": n.p.Dir, "data": data, "settings": settings, "info": info}); err != nil {
		n.kill()
		return fmt.Errorf("плагин не ответил на init: %w", err)
	}
	return nil
}

func (n *nativePlugin) logStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		log.Printf("плагин %s: %s", n.p.Manifest.ID, clip(sc.Text(), 500))
	}
}

func (n *nativePlugin) read(r io.Reader, done chan struct{}) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), nativeLineMax)
	for sc.Scan() {
		var m pluginMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			log.Printf("плагин %s: не JSON в stdout: %s", n.p.Manifest.ID, clip(sc.Text(), 200))
			continue
		}
		if m.Method == "" { // ответ на наш запрос
			n.mu.Lock()
			ch := n.pending[m.ID]
			delete(n.pending, m.ID)
			n.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		go n.handle(m)
	}
}

// handle — запрос или уведомление от плагина.
func (n *nativePlugin) handle(m pluginMsg) {
	var p struct {
		Text     string `json:"text"`
		Silent   bool   `json:"silent"`
		Tools    []any  `json:"tools"`
		Commands bool   `json:"commands"`
	}
	_ = json.Unmarshal(m.Params, &p)
	var result any
	var errText string
	switch m.Method {
	case "register":
		var tools []any
		for _, t := range p.Tools {
			def, _ := t.(map[string]any)
			name, _ := def["name"].(string)
			if def == nil || !toolNameRe.MatchString(name) || isCoreTool(name) {
				log.Printf("плагин %s: инструмент %q отклонён", n.p.Manifest.ID, name)
				continue
			}
			def["type"] = "function"
			tools = append(tools, def)
		}
		n.mu.Lock()
		n.tools, n.commands = tools, p.Commands
		n.mu.Unlock()
		result = true
	case "status":
		n.mu.Lock()
		n.status = clip(p.Text, 200)
		n.mu.Unlock()
	case "log":
		log.Printf("плагин %s: %s", n.p.Manifest.ID, clip(p.Text, 500))
	case "say":
		if n.host.hooks.Say != nil && strings.TrimSpace(p.Text) != "" {
			go n.host.hooks.Say(clip(p.Text, 600))
		}
		result = true
	case "alice":
		ok := n.host.hooks.Alice != nil && strings.TrimSpace(p.Text) != "" && n.host.hooks.Alice(clip(p.Text, 300), p.Silent) == nil
		result = ok
	default:
		errText = "неизвестный метод " + m.Method
	}
	if m.ID != 0 {
		raw, _ := json.Marshal(result)
		_ = n.send(pluginMsg{ID: m.ID, Result: raw, Error: errText})
	}
}

func isCoreTool(name string) bool {
	for _, c := range coreToolNames {
		if c == name {
			return true
		}
	}
	return false
}

func (n *nativePlugin) send(m pluginMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	n.mu.Lock()
	in := n.in
	n.mu.Unlock()
	if in == nil {
		return errors.New("плагин не запущен")
	}
	n.wmu.Lock()
	defer n.wmu.Unlock()
	_, err = in.Write(append(b, '\n'))
	return err
}

func (n *nativePlugin) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	ch := make(chan pluginMsg, 1)
	n.mu.Lock()
	n.nextID++
	id := n.nextID
	n.pending[id] = ch
	done := n.done
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.pending, id); n.mu.Unlock() }()
	if err := n.send(pluginMsg{ID: id, Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != "" {
			return nil, errors.New(m.Error)
		}
		return m.Result, nil
	case <-done:
		return nil, errors.New("плагин завершился")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (n *nativePlugin) exited(err error) {
	n.mu.Lock()
	n.in = nil
	if n.stopped {
		n.mu.Unlock()
		return
	}
	now := time.Now()
	var recent []time.Time
	for _, t := range n.fails {
		if now.Sub(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	n.fails = append(recent, now)
	tries := len(n.fails)
	n.mu.Unlock()
	if tries >= 5 {
		log.Printf("плагин %s: падает раз за разом (%v) — больше не перезапускаю", n.p.Manifest.ID, err)
		n.mu.Lock()
		n.status = "плагин падает раз за разом — выключите и включите его, когда исправят"
		n.mu.Unlock()
		return
	}
	wait := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, time.Minute}[min(tries-1, 3)]
	log.Printf("плагин %s завершился (%v) — перезапуск через %s", n.p.Manifest.ID, err, wait)
	time.AfterFunc(wait, func() {
		n.mu.Lock()
		stopped := n.stopped
		n.mu.Unlock()
		if !stopped {
			if err := n.spawn(); err != nil {
				log.Printf("плагин %s: перезапуск не удался: %v", n.p.Manifest.ID, err)
			}
		}
	})
}

func (n *nativePlugin) kill() {
	n.mu.Lock()
	cmd, done := n.cmd, n.done
	n.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}

func (n *nativePlugin) Tools() []any {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]any(nil), n.tools...)
}

func (n *nativePlugin) CallTool(ctx context.Context, name, args string) (string, bool) {
	n.mu.Lock()
	found := false
	for _, t := range n.tools {
		if def, _ := t.(map[string]any); def["name"] == name {
			found = true
		}
	}
	n.mu.Unlock()
	if !found {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := n.request(ctx, "tool", map[string]any{"name": name, "args": json.RawMessage(orEmpty(args))})
	if err != nil {
		return "Плагин «" + n.p.Manifest.Name + "» не справился: " + clip(err.Error(), 200), true
	}
	var s string
	if json.Unmarshal(res, &s) != nil {
		s = string(res)
	}
	return clip(s, 8000), true
}

func orEmpty(args string) string {
	if strings.TrimSpace(args) == "" || !json.Valid([]byte(args)) {
		return "{}"
	}
	return args
}

func (n *nativePlugin) Command(ctx context.Context, text string) (bool, string) {
	n.mu.Lock()
	want := n.commands
	n.mu.Unlock()
	if !want {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second) // быстрые команды — быстрые: не успел — пусть думает модель
	defer cancel()
	res, err := n.request(ctx, "command", map[string]any{"text": commandText(text), "raw": text})
	if err != nil {
		return false, ""
	}
	var r struct {
		Handled bool   `json:"handled"`
		Answer  string `json:"answer"`
	}
	_ = json.Unmarshal(res, &r)
	return r.Handled, clip(r.Answer, 600)
}

func (n *nativePlugin) Configure(settings map[string]any) {
	n.mu.Lock()
	n.settings = settings
	n.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"settings": settings})
	_ = n.send(pluginMsg{Method: "settings", Params: raw})
}

func (n *nativePlugin) Status() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status
}

func (n *nativePlugin) Stop() {
	n.mu.Lock()
	n.stopped = true
	n.mu.Unlock()
	_ = n.send(pluginMsg{Method: "shutdown"})
	n.mu.Lock()
	done := n.done
	n.mu.Unlock()
	if done != nil {
		select { // даём завершиться самому: сохранить данные, закрыть соединения
		case <-done:
			return
		case <-time.After(2 * time.Second):
		}
	}
	n.kill()
}
