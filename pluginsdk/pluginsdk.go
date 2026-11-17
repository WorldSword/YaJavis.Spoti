// Package pluginsdk — обёртка для нативных плагинов YaJarvis на Go.
//
// Плагин — обычная программа: jarvisd запускает её и говорит с ней строками JSON через stdin/stdout
// (протокол — docs/PLUGINS.md). Пакет прячет протокол:
//
//	func main() {
//		p := pluginsdk.New()
//		p.Tool(pluginsdk.ToolDef{Name: "coin", Description: "Подбросить монетку"}, func(args json.RawMessage) (string, error) {
//			return []string{"орёл", "решка"}[rand.Intn(2)], nil
//		})
//		p.Command(func(text string) (bool, string) {
//			if text == "подбрось монетку" {
//				return true, "Выпал орёл"
//			}
//			return false, ""
//		})
//		p.Run()
//	}
//
// Собирать под колонку: GOOS=linux GOARCH=arm64 и GOOS=linux GOARCH=arm GOARM=7, CGO_ENABLED=0.
package pluginsdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ToolDef — инструмент для модели: имя, описание и JSON Schema параметров.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Init — что ядро сообщает при запуске.
type Init struct {
	API      int            `json:"api"`
	Version  string         `json:"version"`
	Arch     string         `json:"arch"`
	Dir      string         `json:"dir"`  // папка плагина
	Data     string         `json:"data"` // папка для данных (переживает обновление плагина)
	Settings map[string]any `json:"settings"`
	Info     map[string]any `json:"info"` // город, комната, язык, модель колонки
}

type msg struct {
	Deadline int64           `json:"deadline,omitempty"`
	ID       int64           `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type Plugin struct {
	tools       []ToolDef
	handlers    map[string]func(json.RawMessage) (string, error)
	command     func(text string) (bool, string)
	onInit      func(Init)
	onSet       func(map[string]any)
	methods     map[string]func(context.Context, json.RawMessage) (any, error)
	initialized chan struct{}
	initOnce    sync.Once
	running     map[int64]context.CancelFunc

	wmu            sync.Mutex
	nextID         atomic.Int64
	pmu            sync.Mutex
	pending        map[int64]chan msg
	httpMu         sync.Mutex
	httpTransports map[string]*serviceTransport
}

func New() *Plugin {
	return &Plugin{handlers: map[string]func(json.RawMessage) (string, error){}, pending: map[int64]chan msg{}, initialized: make(chan struct{}), running: map[int64]context.CancelFunc{}}
}

// Tool — добавить инструмент. Вызывать до Run.
func (p *Plugin) Tool(def ToolDef, fn func(args json.RawMessage) (string, error)) {
	if def.Parameters == nil {
		def.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	p.tools = append(p.tools, def)
	p.handlers[def.Name] = fn
}

// Command — быстрые команды без модели. text — фраза без «Джарвис», в нижнем регистре, ё → е.
// Вернуть true — фраза ваша (answer Джарвис скажет; пустой — короткий сигнал «готово»). Отвечать быстро: до 3 с.
func (p *Plugin) Command(fn func(text string) (handled bool, answer string)) { p.command = fn }

// Handle — свой метод протокола для того, что плагин «даёт» ядру (provides в plugin.json): например, "vpn".
// Ответ fn уходит ядру как result, ошибка — как error. Вызывать до Run.
func (p *Plugin) Handle(method string, fn func(params json.RawMessage) (any, error)) {
	p.HandleContext(method, func(_ context.Context, params json.RawMessage) (any, error) { return fn(params) })
}

// HandleContext propagates deadlines and cancellation from the core.
func (p *Plugin) HandleContext(method string, fn func(context.Context, json.RawMessage) (any, error)) {
	if p.methods == nil {
		p.methods = map[string]func(context.Context, json.RawMessage) (any, error){}
	}
	p.methods[method] = fn
}

// Notify — уведомление ядру без ответа (например, "vpn" — туннель поменялся сам).
func (p *Plugin) Notify(method string, params any) { p.notify(method, params) }

// OnInit — настройки и сведения о колонке при запуске.
func (p *Plugin) OnInit(fn func(Init)) { p.onInit = fn }

// OnSettings — пользователь поменял настройки в панели.
func (p *Plugin) OnSettings(fn func(map[string]any)) { p.onSet = fn }

// Say — сказать голосом активного ассистента.
func (p *Plugin) Say(text string) { p.notify("say", map[string]any{"text": text}) }

// Status — строка состояния под плагином в панели.
func (p *Plugin) Status(text string) { p.notify("status", map[string]any{"text": text}) }

// Log — строка в журнал Джарвиса (то же, что писать в stderr).
func (p *Plugin) Log(format string, a ...any) {
	p.notify("log", map[string]any{"text": fmt.Sprintf(format, a...)})
}

// Alice — запрос штатной Алисе; silent — без её голосового ответа.
func (p *Plugin) Alice(text string, silent bool) bool {
	res, err := p.request("alice", map[string]any{"text": text, "silent": silent})
	var ok bool
	_ = json.Unmarshal(res, &ok)
	return err == nil && ok
}

func (p *Plugin) write(m msg) {
	b, _ := json.Marshal(m)
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, _ = os.Stdout.Write(append(b, '\n'))
}

func (p *Plugin) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	p.write(msg{Method: method, Params: raw})
}

func (p *Plugin) request(method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.Request(ctx, method, params)
}

// Request calls a method on the core. Cancellation also removes the pending reply.
func (p *Plugin) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	id := p.nextID.Add(1)
	ch := make(chan msg, 1)
	p.pmu.Lock()
	p.pending[id] = ch
	p.pmu.Unlock()
	defer func() { p.pmu.Lock(); delete(p.pending, id); p.pmu.Unlock() }()
	p.write(msg{ID: id, Method: method, Params: raw})
	select {
	case m := <-ch:
		if m.Error != "" {
			return nil, errors.New(m.Error)
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Run — обслуживать ядро, пока оно не попросит завершиться.
func (p *Plugin) Run() {
	if os.Getenv("JARVIS_PLUGIN") != "1" {
		fmt.Fprintln(os.Stderr, "это плагин YaJarvis: его запускает jarvisd, а не человек")
		os.Exit(2)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "" {
			p.pmu.Lock()
			ch := p.pending[m.ID]
			delete(p.pending, m.ID)
			p.pmu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method == "shutdown" {
			return
		}
		if m.Method == "cancel" {
			var in struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(m.Params, &in)
			p.pmu.Lock()
			cancel := p.running[in.ID]
			p.pmu.Unlock()
			if cancel != nil {
				cancel()
			}
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		if m.Deadline > 0 {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.UnixMilli(m.Deadline))
		}
		p.pmu.Lock()
		p.running[m.ID] = cancel
		p.pmu.Unlock()
		go func(m msg, ctx context.Context, cancel context.CancelFunc) {
			defer cancel()
			defer func() { p.pmu.Lock(); delete(p.running, m.ID); p.pmu.Unlock() }()
			p.serve(ctx, m)
		}(m, ctx, cancel)
	}
}

func (p *Plugin) serve(ctx context.Context, m msg) {
	var result any
	var errText string
	if m.Method != "init" {
		select {
		case <-p.initialized:
		case <-ctx.Done():
			return
		}
	}
	switch m.Method {
	case "init":
		var in Init
		_ = json.Unmarshal(m.Params, &in)
		if p.onInit != nil {
			p.onInit(in)
		}
		p.initOnce.Do(func() { close(p.initialized) })
		result = true
		defer p.notify("register", map[string]any{"tools": p.tools, "commands": p.command != nil})
	case "settings":
		var in struct{ Settings map[string]any }
		_ = json.Unmarshal(m.Params, &in)
		if p.onSet != nil {
			p.onSet(in.Settings)
		}
	case "tool":
		var in struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(m.Params, &in)
		if fn := p.handlers[in.Name]; fn != nil {
			s, err := fn(in.Args)
			if err != nil {
				errText = err.Error()
			}
			result = s
		} else {
			errText = "нет инструмента " + in.Name
		}
	case "command":
		var in struct{ Text string }
		_ = json.Unmarshal(m.Params, &in)
		ok, answer := false, ""
		if p.command != nil {
			ok, answer = p.command(in.Text)
		}
		result = map[string]any{"handled": ok, "answer": answer}
	default:
		if fn := p.methods[m.Method]; fn != nil {
			var r any
			var err error
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				r, err = fn(ctx, m.Params)
			}
			if err != nil {
				errText = err.Error()
			}
			result = r
		} else {
			errText = "неизвестный метод " + m.Method
		}
	}
	if m.ID != 0 {
		raw, _ := json.Marshal(result)
		p.write(msg{ID: m.ID, Result: raw, Error: errText})
	}
}
