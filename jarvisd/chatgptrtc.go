package main

// Звонок ChatGPT по подписке: соединение WebRTC готовим заранее, а голос модели проводим через
// буфер против джиттера.
//
// Заранее: создать соединение, собрать сетевые адреса и предложение SDP — это до секунды-трёх, и раньше
// всё это делалось после слова «Джарвис». Теперь при активации остаются запрос к ChatGPT и рукопожатие.
//
// Джиттер: голос идёт пакетами RTP по 20 мс через интернет (часто через VPN) — они приходят неровно,
// иногда не по порядку или теряются. Раньше пакеты декодировались как пришли: перепутанный порядок портил
// декодер, потерянный давал дырку, а пачка после задержки — опустевший плеер. Теперь: порядок по номеру
// пакета, потерянный восстанавливаем из избыточных данных следующего (Opus FEC) или маскируем (PLC),
// а в начале каждой фразы копим запас. Запас подстраивается под сеть: после провала растёт, и следующий звонок
// начинается с того, до чего дорос прошлый (понемногу уменьшаясь, когда сеть успокоилась). Раньше каждый звонок
// стартовал с 80 мс, и на рваном Wi-Fi/VPN голос заикался в начале каждого разговора, пока запас не дорастал.

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/thesyncim/gopus"
)

var chatgptOpus = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}

// rtcPrep — соединение, готовое к звонку: адреса собраны, предложение SDP есть.
type rtcPrep struct {
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticSample
	dc     *webrtc.DataChannel
	opened chan struct{}
	sdp    string
	made   time.Time

	mu   sync.Mutex
	sink func(string) // куда отдавать события канала — назначается, когда соединение берёт звонок
}

func prepareRTC() (*rtcPrep, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: chatgptOpus, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeTCP4})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	p := &rtcPrep{pc: pc, opened: make(chan struct{}), made: time.Now()}
	fail := func(err error) (*rtcPrep, error) { _ = pc.Close(); return nil, err }
	if p.track, err = webrtc.NewTrackLocalStaticSample(chatgptOpus, "audio", "jarvis"); err != nil {
		return fail(err)
	}
	if _, err := pc.AddTrack(p.track); err != nil {
		return fail(err)
	}
	if p.dc, err = pc.CreateDataChannel("oai-events", nil); err != nil {
		return fail(err)
	}
	var once sync.Once
	p.dc.OnOpen(func() { once.Do(func() { close(p.opened) }) })
	p.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		p.mu.Lock()
		sink := p.sink
		p.mu.Unlock()
		if msg.IsString && sink != nil {
			sink(string(msg.Data))
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return fail(err)
	}
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
	}
	p.sdp = pc.LocalDescription().SDP
	return p, nil
}

// Запасное соединение для следующего звонка. Старше 20 минут не берём: мог смениться адрес в сети.
var rtcSpare struct {
	sync.Mutex
	p    *rtcPrep
	busy bool
}

const rtcSpareTTL = 20 * time.Minute

func takeRTC() *rtcPrep {
	rtcSpare.Lock()
	defer rtcSpare.Unlock()
	p := rtcSpare.p
	rtcSpare.p = nil
	if p != nil && time.Since(p.made) > rtcSpareTTL {
		_ = p.pc.Close()
		p = nil
	}
	return p
}

// warmRTC — приготовить запасное соединение в фоне (если его ещё нет или оно устарело).
func warmRTC() {
	rtcSpare.Lock()
	if rtcSpare.busy || (rtcSpare.p != nil && time.Since(rtcSpare.p.made) < rtcSpareTTL-time.Minute) {
		rtcSpare.Unlock()
		return
	}
	rtcSpare.busy = true
	rtcSpare.Unlock()
	go func() {
		p, err := prepareRTC()
		rtcSpare.Lock()
		defer rtcSpare.Unlock()
		rtcSpare.busy = false
		if err != nil {
			log.Printf("GPT-Live (подписка): заготовить соединение не вышло: %v", err)
			return
		}
		if rtcSpare.p != nil {
			_ = rtcSpare.p.pc.Close()
		}
		rtcSpare.p = p
	}()
}

// WarmChatGPTLoop — держать заготовку свежей, пока голос ассистента — ChatGPT по подписке.
func WarmChatGPTLoop(active func() bool) {
	for {
		if active() {
			warmRTC()
		}
		time.Sleep(5 * time.Minute)
	}
}

// ---- буфер против джиттера

const (
	jbFrame   = chatgptRate / 50 // 20 мс
	jbHold    = 3                // сколько пакетов после дырки ждём опоздавший, прежде чем считать его потерянным
	jbPreroll = 6                // пакетов (120 мс) запаса в начале фразы — меньше не начинаем
	jbMaxPre  = 20               // до 400 мс запаса, если сеть (Wi-Fi, VPN) отдаёт пакеты рывками
	jbGrow    = 3                // +60 мс после каждого провала
	jbGap     = 250 * time.Millisecond
)

// jbLearned — до какого запаса дорос прошлый звонок: сеть у колонки обычно та же, и учиться заново
// на каждом разговоре (с заиканием в начале) незачем. Между звонками понемногу отпускаем.
var jbLearned atomic.Int64

func jbStartPreroll() int {
	return max(jbPreroll, min(jbMaxPre, int(jbLearned.Load())-1))
}

// remember — запомнить запас на следующий звонок (вызывается в конце звонка).
func (j *jitterBuf) remember() { jbLearned.Store(int64(j.preroll)) }

type jitterBuf struct {
	dec     *gopus.Decoder
	next    uint16
	started bool
	held    map[uint16]*rtp.Packet
	out     func([]int16)

	spurt   [][]int16 // начало фразы копим, пока не наберётся запас
	lastOut time.Time
	lost    int

	preroll int       // текущий запас в пакетах: растёт после каждого провала
	playT   time.Time // с какого момента идёт непрерывный звук
	playMs  float64   // сколько звука отдано с playT
	under   int       // провалов: плеер опустел посреди фразы
	underMs float64   // их суммарная длина
	now     func() time.Time
}

func newJitterBuf(dec *gopus.Decoder, out func([]int16)) *jitterBuf {
	return &jitterBuf{dec: dec, held: map[uint16]*rtp.Packet{}, out: out, preroll: jbStartPreroll(), now: time.Now}
}

// push — пришёл пакет: декодируем по порядку, дырки закрываем FEC/PLC.
func (j *jitterBuf) push(pkt *rtp.Packet) {
	if !j.started {
		j.started, j.next = true, pkt.SequenceNumber
	}
	if d := int16(pkt.SequenceNumber - j.next); d < 0 {
		return // опоздал — его место уже занято восстановленным звуком
	}
	j.held[pkt.SequenceNumber] = pkt
	if d := pkt.SequenceNumber - j.next; d > 50 { // поток перезапустился — не маскировать сотни «потерь», а начать с самого раннего
		low := pkt.SequenceNumber
		for sq := range j.held {
			if int16(sq-low) < 0 {
				low = sq
			}
		}
		j.next = low
	}
	for {
		if p, ok := j.held[j.next]; ok {
			delete(j.held, j.next)
			j.decode(p.Payload)
			j.next++
			continue
		}
		if len(j.held) < jbHold {
			return
		}
		// пакет j.next потерян: восстановим из избыточных данных следующего, иначе замаскируем
		j.conceal(j.held[j.next+1])
		j.next++
		j.lost++
	}
}

func (j *jitterBuf) decode(payload []byte) {
	buf := make([]int16, chatgptRate*12/100) // самый длинный кадр Opus — 120 мс
	n, err := j.dec.DecodeInt16(payload, buf)
	if err != nil || n <= 0 {
		return
	}
	j.emit(buf[:n])
}

func (j *jitterBuf) conceal(next *rtp.Packet) {
	f := make([]float32, jbFrame)
	var n int
	var err error
	if next != nil {
		n, err = j.dec.DecodeWithFEC(next.Payload, f, true)
	} else {
		n, err = j.dec.DecodeWithFEC(nil, f, false)
	}
	if err != nil || n <= 0 {
		return
	}
	pcm := make([]int16, n)
	for i, v := range f[:n] {
		pcm[i] = int16(max(-32767, min(32767, v*32767)))
	}
	j.emit(pcm)
}

// emit — отдать звук; в начале фразы (после паузы) сначала копим запас, чтобы плеер не пустел на рывках сети.
// Если плеер всё же опустел посреди фразы (пакеты задержались дольше запаса), снова копим запас, уже больший:
// одна пауза чуть длиннее звучит лучше, чем «заикание» на каждом следующем рывке.
func (j *jitterBuf) emit(pcm []int16) {
	now := j.now()
	if j.spurt == nil {
		if now.Sub(j.lastOut) > jbGap {
			j.spurt = [][]int16{}
		} else if late := float64(now.Sub(j.playT).Milliseconds()) - j.playMs; late > 30 {
			j.under++
			j.underMs += late
			j.preroll = min(jbMaxPre, j.preroll+jbGrow)
			j.spurt = [][]int16{}
		}
	}
	j.lastOut = now
	if j.spurt != nil {
		j.spurt = append(j.spurt, pcm)
		if len(j.spurt) < j.preroll {
			return
		}
		j.release(now)
		return
	}
	j.send(pcm)
}

func (j *jitterBuf) release(now time.Time) {
	j.playT, j.playMs = now, 0
	for _, p := range j.spurt {
		j.send(p)
	}
	j.spurt = nil
}

func (j *jitterBuf) send(pcm []int16) {
	j.playMs += float64(len(pcm)) * 1000 / chatgptRate
	j.out(pcm)
}

// flush — поток кончился: отдать накопленное начало фразы, если оно не успело уйти.
func (j *jitterBuf) flush() {
	if j.spurt != nil {
		j.release(j.now())
	}
}

// idle — пакетов давно нет. Короткую фразу, которая кончилась раньше, чем набрался запас, отдаём —
// но только если тишина дольше самого запаса: иначе это задержка сети посреди начала фразы, и отданный
// раньше времени неполный запас сразу кончится провалом.
func (j *jitterBuf) idle(since time.Duration) {
	if since > time.Duration(j.preroll*20+60)*time.Millisecond {
		j.flush()
	}
}
