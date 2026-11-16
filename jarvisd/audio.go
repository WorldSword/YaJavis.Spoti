package main

// Звук колонки: микрофон — обработанный канал vqe_0 (эхо- и шумоподавление Яндекса) через наш
// vqe_tap.so в maind; динамик — aplay в тот же ALSA-микшер, что и у Алисы.

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const MicRate = 16000

// Mic принимает датаграммы PCM s16le 16 кГц от vqe_tap.so и раздаёт их подписчикам.
type Mic struct {
	path string
	mu   sync.Mutex
	subs map[int]chan []int16
	next int
	Seen time.Time // последний кадр — для статуса «микрофон на связи»
	// GainDB — общее усиление (дБ) для всех подписчиков: слово активации, ИИ, записи для обучения.
	// На Мини 2 обработка Алисы (профиль yandexmini_2) отдаёт голос заметно тише, чем на Лайт.
	GainDB func() int
}

func NewMic(path string) *Mic { return &Mic{path: path, subs: map[int]chan []int16{}} }

func (m *Mic) Subscribe(buf int) (int, <-chan []int16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	ch := make(chan []int16, buf)
	m.subs[m.next] = ch
	return m.next, ch
}

func (m *Mic) Unsubscribe(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.subs[id]; ok {
		close(ch)
		delete(m.subs, id)
	}
}

func (m *Mic) Online() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Since(m.Seen) < 3*time.Second
}

// gainPCM — усилить на месте; громкие места мягко прижимаются (softClip), а не обрезаются с треском.
func gainPCM(pcm []int16, db int) {
	if db <= 0 {
		return
	}
	k := math.Pow(10, float64(db)/20)
	for i, v := range pcm {
		x := float64(v) * k
		if x > 32767 || x < -32767 { // за пределами — гарантированно в потолок
			x = max(-40000, min(40000, x))
		}
		pcm[i] = softClip(x)
	}
}

func (m *Mic) Run() {
	for {
		if err := m.serve(); err != nil {
			log.Printf("микрофон: %v — повтор через 3 с", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func (m *Mic) serve() error {
	_ = os.MkdirAll(dirOf(m.path), 0o755)
	_ = os.Remove(m.path)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: m.path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = os.Chmod(m.path, 0o666) // maind пишет от root, но на других прошивках может быть другой пользователь
	_ = conn.SetReadBuffer(1 << 18)
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}
		if n < 2 {
			continue
		}
		pcm := make([]int16, n/2)
		for i := range pcm {
			pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		if m.GainDB != nil {
			gainPCM(pcm, m.GainDB())
		}
		m.mu.Lock()
		m.Seen = time.Now()
		for _, ch := range m.subs {
			select {
			case ch <- pcm:
			default: // медленный подписчик не тормозит остальных
			}
		}
		m.mu.Unlock()
	}
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// Player — один ответ = один процесс aplay. Стоп — SIGINT: aplay сам корректно отцепляется от dmix
// (SIGKILL посреди записи оставлял dmix в состоянии, где следующий aplay висел).
type Player struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	queue  chan []byte
	done   chan struct{}
	exited chan struct{}  // only the Start goroutine calls cmd.Wait
	Volume func() int     // 0..100, читается на каждом куске
	Trim   func() float64 // поправка к громкости голоса от громкости самой колонки (1 — без поправки)
	lim    *limiter
	up     *upsampler // 16/24 кГц → 48 кГц своим фильтром, а не линейным пересчётом ALSA
	boost  float64    // доп. усиление голоса конкретного движка (ChatGPT по подписке приходит тише)
	aplay  string

	OnStart func() // перед запуском aplay (может ждать); к каждому OnStart придёт ровно один OnExit
	OnExit  func() // aplay завершился или не запустился
}

func NewPlayer() *Player {
	p := &Player{aplay: "/usr/bin/aplay"}
	if _, err := os.Stat(p.aplay); err != nil {
		p.aplay = "aplay"
	}
	return p
}

func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// Start открывает плеер; rate — частота PCM (моно s16le).
func (p *Player) Start(rate int) error {
	p.Stop(true)
	if p.OnStart != nil {
		p.OnStart()
	}
	err := p.start(rate)
	if err != nil && p.OnExit != nil {
		p.OnExit()
	}
	return err
}

func (p *Player) start(rate int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var up *upsampler
	out := rate
	if rate > 0 && rate < OutRate && OutRate%rate == 0 {
		up, out = newUpsampler(OutRate/rate), OutRate
	}
	cmd := exec.Command(p.aplay, "-q", "-D", "default", "-t", "raw", "-f", "S16_LE", "-r", itoa(out), "-c", "1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd, p.in = cmd, in
	p.lim, p.up, p.boost = newLimiter(rate), up, 1
	p.queue = make(chan []byte, 512)
	p.done = make(chan struct{})
	p.exited = make(chan struct{})
	exited := p.exited
	onExit := p.OnExit
	go func() {
		_ = cmd.Wait()
		close(exited)
		if onExit != nil {
			onExit()
		}
	}()
	go p.pump(in, p.queue, p.done)
	return nil
}

// StartVoice — плеер для голоса модели: с усилением, если движок его просит (OutBoost).
func (p *Player) StartVoice(sess interface{ Rate() int }) error {
	if err := p.Start(sess.Rate()); err != nil {
		return err
	}
	if b, ok := sess.(interface{ OutBoost() float64 }); ok {
		p.SetBoost(b.OutBoost())
	}
	return nil
}

func (p *Player) SetBoost(b float64) {
	p.mu.Lock()
	p.boost = b
	p.mu.Unlock()
}

func (p *Player) pump(in io.WriteCloser, q chan []byte, done chan struct{}) {
	defer close(done)
	for b := range q {
		if _, err := in.Write(b); err != nil {
			for range q { // плеер умер — выбрасываем остаток
			}
			return
		}
	}
	_ = in.Close()
}

// playGain — усиление голоса Джарвиса при громкости 100% в настройках. Голос GPT приходит тихим (речь около
// −22 дБ от полной шкалы), а Алиса сведена громко и идёт через тот же регулятор громкости колонки, поэтому на
// одной и той же громкости Джарвиса было почти не слышно. ×6,5 с ограничителем даёт речь около −8 дБ — чуть громче Алисы.
const playGain = 6.5

// limiter — ограничитель с опережением на 5 мс: громкие слоги заранее плавно прижимаются к потолку, тихие
// усиливаются полностью. Так голос становится громче без треска, который дало бы простое умножение.
type limiter struct {
	buf  []float64
	pos  int
	gain float64
}

func newLimiter(rate int) *limiter {
	return &limiter{buf: make([]float64, max(16, rate/200)), gain: 1}
}

func (l *limiter) next(x float64) int16 {
	const ceil = 29000.0
	t := 1.0
	if a := math.Abs(x); a > ceil {
		t = ceil / a
	}
	if t < l.gain {
		l.gain += (t - l.gain) * 0.1 // успевает за время опережения
	} else {
		l.gain += (t - l.gain) / (0.12 * 200 * float64(len(l.buf))) // отпускает за ~0.12 с
	}
	y := l.buf[l.pos] * l.gain
	l.buf[l.pos] = x
	l.pos = (l.pos + 1) % len(l.buf)
	return softClip(y)
}

// softClip — страховка после ограничителя: редкие остаточные пики плавно прижимаются к потолку.
func softClip(x float64) int16 {
	const knee, top = 29000.0, 32767.0
	switch {
	case x > knee:
		x = knee + (top-knee)*math.Tanh((x-knee)/(top-knee))
	case x < -knee:
		x = -knee - (top-knee)*math.Tanh((-x-knee)/(top-knee))
	}
	return int16(x)
}

// Write — кусок PCM; не блокирует (если aplay завис, ответ просто теряется, а не встаёт всё).
func (p *Player) Write(pcm []int16) error {
	vol := 100
	if p.Volume != nil {
		vol = p.Volume()
	}
	g := float64(vol) / 50 * playGain // 50% — прежний максимум, 100% — вдвое громче
	if p.Trim != nil {
		g *= p.Trim()
	}
	p.mu.Lock()
	if p.boost > 0 {
		g *= p.boost
	}
	defer p.mu.Unlock() // отправка тоже под замком: Stop/Finish закрывают канал под ним же — иначе «send on closed channel»
	q, lim := p.queue, p.lim
	if q == nil || lim == nil {
		return errors.New("плеер не запущен")
	}
	shaped := make([]int16, len(pcm))
	for i, s := range pcm {
		shaped[i] = lim.next(float64(s) * g)
	}
	if p.up != nil {
		shaped = p.up.run(shaped)
	}
	b := make([]byte, 2*len(shaped))
	for i, s := range shaped {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	select {
	case q <- b: // не блокирует: при полном канале — default
		return nil
	default:
		return errors.New("плеер не успевает")
	}
}

// Finish — доиграть то, что уже отдано, и закрыть.
func (p *Player) Finish(timeout time.Duration) {
	p.mu.Lock()
	cmd, q, done, exited := p.cmd, p.queue, p.done, p.exited
	p.queue = nil
	if q != nil {
		close(q) // повторный Finish (или Finish после Stop) канал уже закрыл
	}
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
	}
	waitPlayer(cmd, exited, timeout)
	p.mu.Lock()
	if p.cmd == cmd {
		p.cmd = nil
	}
	p.mu.Unlock()
}

// Stop — оборвать сразу (now) или дать доиграть буфер ALSA.
func (p *Player) Stop(now bool) {
	p.mu.Lock()
	cmd, q, exited := p.cmd, p.queue, p.exited
	p.cmd, p.queue = nil, nil
	if q != nil {
		close(q)
	}
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	if now {
		_ = cmd.Process.Signal(syscall.SIGINT)
	}
	waitPlayer(cmd, exited, 2*time.Second)
}

func waitPlayer(cmd *exec.Cmd, exited <-chan struct{}, timeout time.Duration) {
	select {
	case <-exited:
	case <-time.After(timeout):
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-exited:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
		}
	}
}

func waitOrInt(cmd *exec.Cmd, timeout time.Duration) {
	ch := make(chan struct{})
	go func() { _ = cmd.Wait(); close(ch) }()
	select {
	case <-ch:
	case <-time.After(timeout):
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-ch:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// writeWAV — моно 16 кГц s16le.
func writeWAV(path string, pcm []int16) error {
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], MicRate)
	binary.LittleEndian.PutUint32(b[28:], MicRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(v))
	}
	return os.WriteFile(path, b, 0o600)
}
