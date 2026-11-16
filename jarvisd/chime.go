package main

// Сигнал активации: когда колонка услышала «Джарвис», играет тот же короткий «тилиньк», что и у Алисы.
// Звук берётся с самой колонки из прошивки Алисы (в репозиторий и в бинарник он не входит), декодируется один раз.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// wakeChimePath — «spotter_start»: звук, который Алиса играет, услышав слово активации.
const wakeChimePath = "/system/vendor/quasar/sounds/common/spotter_start.mp3"

type Chime struct {
	Path   string
	Volume func() int // громкость голоса колонки 0..100; nil — полная
	run    func(ctx context.Context, rate int, pcm []byte) error

	mu   sync.Mutex
	pcm  []byte // готовые отсчёты на выходной частоте
	done bool
}

func NewChime(volume func() int) *Chime {
	return &Chime{Path: wakeChimePath, Volume: volume, run: playRaw}
}

// Play проигрывает сигнал и возвращается, когда он доигран. Ошибка — не повод ломать разговор: вызывающий её только пишет в журнал.
func (c *Chime) Play(ctx context.Context) error {
	pcm, err := c.load()
	if err != nil {
		return err
	}
	gain := 1.0
	if c.Volume != nil {
		gain = float64(c.Volume()) / 100
	}
	out := make([]byte, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) * gain
		binary.LittleEndian.PutUint16(out[i:], uint16(clip16(v)))
	}
	return c.run(ctx, OutRate, out)
}

func (c *Chime) load() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return c.pcm, nil
	}
	f, err := os.Open(c.Path)
	if err != nil {
		return nil, fmt.Errorf("нет звука активации: %w", err)
	}
	defer f.Close()
	mono, rate, err := decodeMP3(f)
	if err != nil {
		return nil, fmt.Errorf("не разобрал звук активации: %w", err)
	}
	mono = resampleLinear(mono, rate, OutRate)
	b := make([]byte, 2*len(mono))
	for i, s := range mono {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	c.pcm, c.done = b, true
	return b, nil
}

func clip16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// decodeMP3 — MP3 в моно s16 и его частота. Декодер выдаёт стерео, берём среднее каналов.
func decodeMP3(r io.Reader) ([]int16, int, error) {
	d, err := mp3.NewDecoder(r)
	if err != nil {
		return nil, 0, err
	}
	raw, err := io.ReadAll(io.LimitReader(d, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	n := len(raw) / 4
	if n == 0 {
		return nil, 0, errors.New("в файле нет звука")
	}
	mono := make([]int16, n)
	for i := range mono {
		l := int16(binary.LittleEndian.Uint16(raw[4*i:]))
		rr := int16(binary.LittleEndian.Uint16(raw[4*i+2:]))
		mono[i] = int16((int(l) + int(rr)) / 2)
	}
	return mono, d.SampleRate(), nil
}

// resampleLinear — смена частоты линейной интерполяцией; для короткого сигнала качества достаточно.
func resampleLinear(in []int16, from, to int) []int16 {
	if from == to || from <= 0 || to <= 0 || len(in) < 2 {
		return append([]int16(nil), in...)
	}
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]int16, n)
	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		j := int(pos)
		if j >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		f := pos - float64(j)
		out[i] = int16(float64(in[j])*(1-f) + float64(in[j+1])*f)
	}
	return out
}

// playRaw отдельным aplay играет моно s16le: не трогает плеер голоса, так что идущий ответ не прерывается.
func playRaw(ctx context.Context, rate int, pcm []byte) error {
	aplay := "/usr/bin/aplay"
	if _, err := os.Stat(aplay); err != nil {
		aplay = "aplay"
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, aplay, "-q", "-D", "default", "-t", "raw", "-f", "S16_LE", "-r", itoa(rate), "-c", "1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, werr := in.Write(pcm)
	in.Close()
	if err := cmd.Wait(); err != nil {
		return err
	}
	return werr
}
