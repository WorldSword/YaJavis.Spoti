package main

// JS-плагины: скрипт выполняется встроенным goja (тот же движок, что разбирает плеер YouTube). Плагину доступен
// только объект jarvis — сеть, файлы и процессы колонки ему не видны. Всё синхронно: fetch возвращает ответ сразу,
// без await (async-функции тоже работают). Один плагин — один движок и один замок: вызовы идут по очереди.
// Каждый вызов ограничен по времени, чтобы зависший скрипт не держал Джарвиса.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const (
	jsCallLimit  = 20 * time.Second // один вызов инструмента или команды
	jsLoadLimit  = 5 * time.Second  // загрузка скрипта
	jsFetchMax   = 2 << 20          // ответ fetch
	jsStoreMax   = 256 << 10        // данные плагина (data.json)
	jsEveryFloor = 60               // секунд: чаще фоновые задачи не запускаем
)

var toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// coreToolNames — имена инструментов ядра: плагин не может их подменить.
var coreToolNames = []string{"smart_home", "play_music", "music_control", "alice_command", "recall_conversation",
	"edit_memory", "home_assistant", "xiaomi_home", "smart_life"}

type jsTool struct {
	def map[string]any
	fn  goja.Callable
}

type jsCommand struct {
	re *regexp.Regexp
	fn goja.Callable
}

type jsPlugin struct {
	host *PluginHost
	p    *Plugin

	mu       sync.Mutex // goja не потокобезопасен: один вызов скрипта за раз
	vm       *goja.Runtime
	ctx      context.Context // контекст текущего вызова (для fetch)
	reg      sync.Mutex      // список инструментов, команд и статус — читаются, пока скрипт занят
	tools    map[string]jsTool
	order    []string
	commands []jsCommand
	onSet    goja.Callable
	status   string
	stopped  chan struct{}
	stopOnce sync.Once
	data     map[string]any
}

func startJSPlugin(h *PluginHost, p *Plugin, settings map[string]any) (pluginRuntime, error) {
	src, err := os.ReadFile(filepath.Join(p.Dir, p.Manifest.Main))
	if err != nil {
		return nil, err
	}
	j := &jsPlugin{host: h, p: p, vm: goja.New(), tools: map[string]jsTool{}, stopped: make(chan struct{}), ctx: context.Background()}
	j.vm.SetFieldNameMapper(goja.UncapFieldNameMapper())
	if b, err := os.ReadFile(pluginDataPath(p)); err == nil {
		_ = json.Unmarshal(b, &j.data)
	}
	if j.data == nil {
		j.data = map[string]any{}
	}
	j.install(settings)
	j.mu.Lock()
	timer := time.AfterFunc(jsLoadLimit, func() { j.vm.Interrupt("загрузка дольше 5 с") })
	_, err = j.vm.RunScript(p.Manifest.Main, string(src))
	timer.Stop()
	j.vm.ClearInterrupt()
	j.mu.Unlock()
	if err != nil {
		j.Stop() // фоновые задачи jarvis.every, заведённые до ошибки, не должны жить без плагина
		return nil, jsErr(err)
	}
	return j, nil
}

func jsErr(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return errors.New(strings.TrimSpace(ex.String()))
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return fmt.Errorf("скрипт прерван: %v", ie.Value())
	}
	return err
}

func (j *jsPlugin) throw(msg string) {
	panic(j.vm.NewGoError(errors.New(msg)))
}

// install — объект jarvis для скрипта.
func (j *jsPlugin) install(settings map[string]any) {
	vm := j.vm
	jv := vm.NewObject()
	_ = jv.Set("api", pluginAPI)
	_ = jv.Set("version", version)
	_ = jv.Set("arch", pluginArch())
	_ = jv.Set("settings", vm.ToValue(settings))
	_ = jv.Set("log", func(c goja.FunctionCall) goja.Value {
		var parts []string
		for _, a := range c.Arguments {
			parts = append(parts, jsString(a))
		}
		log.Printf("плагин %s: %s", j.p.Manifest.ID, strings.Join(parts, " "))
		return goja.Undefined()
	})
	_ = jv.Set("tool", func(def map[string]any, fn goja.Value) {
		f, ok := goja.AssertFunction(fn)
		if !ok {
			j.throw("jarvis.tool(описание, функция): нет функции")
		}
		name, _ := def["name"].(string)
		if !toolNameRe.MatchString(name) || slices.Contains(coreToolNames, name) {
			j.throw("jarvis.tool: имя инструмента — латиница, цифры, _ и -, и не занято Джарвисом: " + name)
		}
		desc, _ := def["description"].(string)
		params, _ := def["parameters"].(map[string]any)
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		j.reg.Lock()
		defer j.reg.Unlock()
		if _, dup := j.tools[name]; !dup {
			j.order = append(j.order, name)
		}
		j.tools[name] = jsTool{def: map[string]any{"type": "function", "name": name, "description": desc, "parameters": params}, fn: f}
	})
	_ = jv.Set("command", func(patterns goja.Value, fn goja.Value) {
		f, ok := goja.AssertFunction(fn)
		if !ok {
			j.throw("jarvis.command(шаблоны, функция): нет функции")
		}
		var list []string
		switch v := patterns.Export().(type) {
		case string:
			list = []string{v}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					list = append(list, s)
				}
			}
		}
		for _, s := range list {
			re, err := regexp.Compile("^(?:" + s + ")$")
			if err != nil {
				j.throw("jarvis.command: шаблон «" + s + "»: " + err.Error())
			}
			j.reg.Lock()
			j.commands = append(j.commands, jsCommand{re, f})
			j.reg.Unlock()
		}
	})
	_ = jv.Set("onSettings", func(fn goja.Value) {
		if f, ok := goja.AssertFunction(fn); ok {
			j.reg.Lock()
			j.onSet = f
			j.reg.Unlock()
		}
	})
	_ = jv.Set("status", func(s string) { j.reg.Lock(); j.status = clip(s, 200); j.reg.Unlock() })
	_ = jv.Set("info", func() map[string]any {
		if j.host.hooks.Info != nil {
			return j.host.hooks.Info()
		}
		return map[string]any{}
	})
	_ = jv.Set("fetch", j.fetch)
	_ = jv.Set("say", func(text string) {
		if j.host.hooks.Say != nil && strings.TrimSpace(text) != "" {
			go j.host.hooks.Say(clip(text, 600))
		}
	})
	_ = jv.Set("alice", func(text string, silent bool) bool {
		if j.host.hooks.Alice == nil || strings.TrimSpace(text) == "" {
			return false
		}
		return j.host.hooks.Alice(clip(text, 300), silent) == nil
	})
	_ = jv.Set("every", func(seconds int, fn goja.Value) {
		f, ok := goja.AssertFunction(fn)
		if !ok {
			j.throw("jarvis.every(секунды, функция): нет функции")
		}
		go j.every(max(seconds, jsEveryFloor), f)
	})
	store := vm.NewObject()
	_ = store.Set("get", func(key string) any { return j.data[key] })
	_ = store.Set("set", func(key string, v goja.Value) {
		if goja.IsUndefined(v) || goja.IsNull(v) {
			delete(j.data, key)
		} else {
			j.data[key] = v.Export()
		}
		b, err := json.Marshal(j.data)
		if err != nil {
			j.throw("jarvis.store.set: значение не сохраняется в JSON")
		}
		if len(b) > jsStoreMax {
			delete(j.data, key)
			j.throw(fmt.Sprintf("jarvis.store: данных больше %d КБ", jsStoreMax>>10))
		}
		_ = os.WriteFile(pluginDataPath(j.p), b, 0o600)
	})
	_ = store.Set("all", func() map[string]any { return j.data })
	_ = jv.Set("store", store)
	_ = vm.Set("jarvis", jv)
	_ = vm.Set("console", map[string]any{"log": jv.Get("log")})
}

func jsString(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	switch x := v.Export().(type) {
	case string:
		return x
	case map[string]any, []any:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
	}
	return v.String()
}

// fetch(url, {method, headers, body, timeout}) → {status, ok, text, headers, json()}
func (j *jsPlugin) fetch(link string, opts map[string]any) map[string]any {
	if !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
		j.throw("jarvis.fetch: нужен адрес http(s)://")
	}
	method, _ := opts["method"].(string)
	if method == "" {
		method = http.MethodGet
	}
	timeout := 15 * time.Second
	switch t := opts["timeout"].(type) {
	case int64:
		if t > 0 && t <= 60 {
			timeout = time.Duration(t) * time.Second
		}
	case float64:
		if t > 0 && t <= 60 {
			timeout = time.Duration(t * float64(time.Second))
		}
	}
	var body io.Reader
	switch b := opts["body"].(type) {
	case string:
		body = strings.NewReader(b)
	case nil:
	default:
		raw, _ := json.Marshal(b)
		body = strings.NewReader(string(raw))
	}
	ctx, cancel := context.WithTimeout(j.ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), link, body)
	if err != nil {
		j.throw("jarvis.fetch: " + err.Error())
	}
	req.Header.Set("User-Agent", "YaJarvis-plugin/"+j.p.Manifest.ID)
	if hs, ok := opts["headers"].(map[string]any); ok {
		for k, v := range hs {
			req.Header.Set(k, fmt.Sprint(v))
		}
	}
	client := http.DefaultClient
	if j.host.hooks.Client != nil {
		client = j.host.hooks.Client(timeout)
	}
	resp, err := client.Do(req)
	if err != nil {
		j.throw("jarvis.fetch: " + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, jsFetchMax))
	text := string(raw)
	hdr := map[string]any{}
	for k := range resp.Header {
		hdr[strings.ToLower(k)] = resp.Header.Get(k)
	}
	vm := j.vm
	return map[string]any{"status": resp.StatusCode, "ok": resp.StatusCode < 300, "text": text, "headers": hdr,
		"json": func() goja.Value {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				j.throw("jarvis.fetch: ответ не JSON")
			}
			return vm.ToValue(v)
		}}
}

// call — выполнить функцию скрипта под замком и с ограничением времени; Promise дожидается.
func (j *jsPlugin) call(ctx context.Context, f goja.Callable, args ...any) (goja.Value, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	select {
	case <-j.stopped:
		return nil, errors.New("плагин остановлен")
	default:
	}
	j.ctx = ctx
	defer func() { j.ctx = context.Background() }()
	vals := make([]goja.Value, len(args))
	for i, a := range args {
		vals[i] = j.vm.ToValue(a)
	}
	timer := time.AfterFunc(jsCallLimit, func() { j.vm.Interrupt("дольше 20 с") })
	stop := context.AfterFunc(ctx, func() { j.vm.Interrupt("отменено") })
	v, err := f(goja.Undefined(), vals...)
	timer.Stop()
	stop()
	j.vm.ClearInterrupt()
	if err != nil {
		return nil, jsErr(err)
	}
	if p, ok := v.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateFulfilled:
			return p.Result(), nil
		case goja.PromiseStateRejected:
			return nil, errors.New(jsString(p.Result()))
		default:
			return nil, errors.New("async-функция не завершилась (await чего-то, кроме jarvis.*)")
		}
	}
	return v, nil
}

func (j *jsPlugin) Tools() []any {
	j.reg.Lock()
	defer j.reg.Unlock()
	out := make([]any, 0, len(j.order))
	for _, name := range j.order {
		out = append(out, j.tools[name].def)
	}
	return out
}

func (j *jsPlugin) CallTool(ctx context.Context, name, args string) (string, bool) {
	j.reg.Lock()
	t, ok := j.tools[name]
	j.reg.Unlock()
	if !ok {
		return "", false
	}
	var a any = map[string]any{}
	if strings.TrimSpace(args) != "" {
		_ = json.Unmarshal([]byte(args), &a)
	}
	v, err := j.call(ctx, t.fn, a)
	if err != nil {
		log.Printf("плагин %s: %s: %v", j.p.Manifest.ID, name, err)
		return "Плагин «" + j.p.Manifest.Name + "» не справился: " + clip(err.Error(), 200), true
	}
	return clip(jsString(v), 8000), true
}

// Command — фраза (без «Джарвис», в нижнем регистре, ё → е) подошла под шаблон плагина.
// Функция вернула строку — команда выполнена (строку Джарвис скажет), null/undefined — не моя фраза.
func (j *jsPlugin) Command(ctx context.Context, text string) (bool, string) {
	n := commandText(text)
	j.reg.Lock()
	cmds := slices.Clone(j.commands)
	j.reg.Unlock()
	for _, c := range cmds {
		m := c.re.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		v, err := j.call(ctx, c.fn, n, m[1:])
		if err != nil {
			log.Printf("плагин %s: команда «%s»: %v", j.p.Manifest.ID, n, err)
			if why := jsReason(err); why != "" { // «не знаю город: укажите его в настройках» — человеку полезнее, чем «не справился»
				return true, j.p.Manifest.Name + ": " + why
			}
			return true, "Плагин «" + j.p.Manifest.Name + "» не справился."
		}
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			continue
		}
		return true, clip(jsString(v), 600)
	}
	return false, ""
}

// jsReason — текст ошибки скрипта для человека: первая строка, без «Error:» и стека; прерывания и сбои движка — пусто.
func jsReason(err error) string {
	msg := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	for _, pre := range []string{"Error: ", "GoError: ", "TypeError: ", "RangeError: "} {
		if strings.HasPrefix(msg, pre) {
			if pre != "Error: " && pre != "GoError: " {
				return "" // ошибка в коде плагина, а не понятная причина
			}
			msg = strings.TrimPrefix(msg, pre)
		}
	}
	if msg == "" || strings.HasPrefix(msg, "скрипт прерван") || strings.HasPrefix(msg, "ReferenceError") || strings.HasPrefix(msg, "SyntaxError") {
		return ""
	}
	return clip(msg, 200)
}

var cmdPunct = regexp.MustCompile(`[^\p{L}\p{N}\s-]+`)

// commandText — фраза для шаблонов плагинов: без имени, нижний регистр, ё → е, без знаков препинания.
func commandText(text string) string {
	s := strings.ToLower(canonName(text))
	s = strings.ReplaceAll(s, "ё", "е")
	s = cmdPunct.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	for _, name := range []string{"джарвис ", "jarvis "} {
		s = strings.TrimPrefix(s, name)
	}
	return strings.TrimSpace(s)
}

func (j *jsPlugin) Configure(settings map[string]any) {
	j.mu.Lock()
	_ = j.vm.Get("jarvis").ToObject(j.vm).Set("settings", j.vm.ToValue(settings))
	j.mu.Unlock()
	j.reg.Lock()
	f := j.onSet
	j.reg.Unlock()
	if f != nil {
		if _, err := j.call(context.Background(), f, settings); err != nil {
			log.Printf("плагин %s: onSettings: %v", j.p.Manifest.ID, err)
		}
	}
}

func (j *jsPlugin) every(seconds int, f goja.Callable) {
	t := time.NewTicker(time.Duration(seconds) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-j.stopped:
			return
		case <-t.C:
			if _, err := j.call(context.Background(), f); err != nil {
				log.Printf("плагин %s: фоновая задача: %v", j.p.Manifest.ID, err)
			}
		}
	}
}

func (j *jsPlugin) Status() string {
	j.reg.Lock()
	defer j.reg.Unlock()
	return j.status
}

func (j *jsPlugin) Stop() {
	j.stopOnce.Do(func() {
		close(j.stopped)
		j.vm.Interrupt("плагин остановлен")
	})
}
