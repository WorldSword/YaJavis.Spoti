package main

import (
	"math"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/thesyncim/gopus"
)

// Пакеты не по порядку и с потерей: на выходе — все 20-мс куски по порядку, потерянный восстановлен.
func TestJitterBufReorderAndLoss(t *testing.T) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: 48000, Channels: 1, Application: gopus.ApplicationVoIP})
	if err != nil {
		t.Fatal(err)
	}
	var pkts []*rtp.Packet
	for i := 0; i < 12; i++ {
		pcm := make([]int16, 960) // 20 мс при 48 кГц
		for k := range pcm {
			pcm[k] = int16(8000 * math.Sin(2*math.Pi*440*float64(i*960+k)/48000))
		}
		data, err := enc.EncodeInt16Slice(pcm)
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(65530 + i)}, Payload: data}) // и переход через 65535
	}
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(chatgptRate, 1))
	if err != nil {
		t.Fatal(err)
	}
	var frames int
	jb := newJitterBuf(dec, func(pcm []int16) { frames++ })
	order := []int{0, 2, 1, 3, 4 /* 5 потерян */, 6, 7, 8, 9, 10, 11, 1 /* дубликат-опоздавший */}
	for _, i := range order {
		jb.push(pkts[i])
	}
	jb.flush()
	if jb.lost != 1 {
		t.Fatalf("потерян один пакет, насчитали %d", jb.lost)
	}
	// 12 кусков: 11 настоящих + 1 восстановленный; дубликат не повторяется. Последние jbHold-1 ждут продолжения.
	if want := 12 - (jbHold - 1); frames < want-1 || frames > 12 {
		t.Fatalf("кусков звука %d, ждали около %d", frames, want)
	}
}

// Рывок сети посреди фразы: провал засчитан, запас растёт, звук после рывка снова копится, а не «заикается».
func TestJitterBufUnderrunGrowsPreroll(t *testing.T) {
	clock := time.Unix(0, 0)
	var got int
	jb := newJitterBuf(nil, func([]int16) { got++ })
	jb.now = func() time.Time { return clock }
	frame := make([]int16, jbFrame)
	for i := 0; i < 10; i++ { // ровно по 20 мс
		jb.emit(frame)
		clock = clock.Add(20 * time.Millisecond)
	}
	if jb.under != 0 || got != 10 {
		t.Fatalf("ровный поток: провалов %d, кусков %d", jb.under, got)
	}
	clock = clock.Add(200 * time.Millisecond) // пакеты задержались, запас 80 мс кончился
	jb.emit(frame)
	if jb.under != 1 || jb.preroll != jbPreroll+jbGrow {
		t.Fatalf("после рывка: провалов %d, запас %d", jb.under, jb.preroll)
	}
	if got != 10 {
		t.Fatalf("после провала звук должен снова копиться, а ушло %d", got)
	}
	for i := 0; i < jbPreroll+jbGrow-1; i++ {
		jb.emit(frame)
	}
	if got != 10+jbPreroll+jbGrow {
		t.Fatalf("после запаса отдано %d", got)
	}
}
