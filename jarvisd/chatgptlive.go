package main

// GPT-Live по подписке ChatGPT: тот же голос, что у ключа API, но разговор идёт как у Codex —
// звонком WebRTC (голос кодеком Opus), а события и ответы бэкенда — по каналу данных звонка.
// Думает и управляет домом наш собственный «мозг»: голосовая модель передаёт ему задачу
// (delegation), мы отвечаем текстом, она произносит.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/thesyncim/gopus"
)

const (
	chatgptCallURL   = "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas"
	chatgptLiveModel = "gpt-live-1-codex"
)

// ChatGPTVoices — голоса GPT-Live в подписке (у ключа API набор другой).
var ChatGPTVoices = []string{"cove", "juniper", "maple", "spruce", "ember", "vale", "breeze", "arbor", "sol"}

var ChatGPTVoiceGroups = []VoiceGroup{{"Подписка ChatGPT", []VoiceInfo{
	{"cove", "спокойный"}, {"juniper", "светлый"}, {"maple", "тёплый"}, {"spruce", "деловой"}, {"ember", "живой"},
	{"vale", "мягкий"}, {"breeze", "лёгкий"}, {"arbor", "низкий"}, {"sol", "яркий"}}}}

func chatgptVoice(v string) string {
	v = strings.ToLower(v)
	for _, x := range ChatGPTVoices {
		if x == v {
			return v
		}
	}
	return "cove"
}

// Brain — ответ на задачу, которую голосовая модель передала бэкенду.
type Brain func(ctx context.Context, task string) (string, error)

type ChatGPTSession struct {
	dialog
	auth         ChatGPTAuth
	getAuth      func(context.Context) (ChatGPTAuth, error)
	prof         Profile
	instructions string
	client       *http.Client
	brain        Brain

	ctx     context.Context
	cancel  context.CancelFunc
	started chan error
	once    sync.Once
	players sync.WaitGroup

	pc    *webrtc.PeerConnection
	dc    *webrtc.DataChannel
	track *webrtc.TrackLocalStaticSample
	dcMu  sync.Mutex

	pendingMu sync.Mutex
	pending   []int16
	Out       chan []int16
	gain      float64

	// для журнала: что происходило за звонок (если модель «молчит», видно, кто кого не слышал)
	statMu   sync.Mutex
	events   map[string]int
	sentLoud int // кадров речи (20 мс) ушло модели
	gotLoud  int // кусков голоса пришло от модели
	level    levelMeter
}

func (s *ChatGPTSession) stat(f func()) {
	s.statMu.Lock()
	if s.events == nil {
		s.events = map[string]int{}
	}
	f()
	s.statMu.Unlock()
}

func (s *ChatGPTSession) Audio() <-chan []int16 { return s.Out }
func (s *ChatGPTSession) Rate() int             { return chatgptRate }

// chatgptRate — голос ChatGPT приходит в Opus 48 кГц; раньше декодировали в 16 кГц, и он звучал как по телефону.
const chatgptRate = OutRate

// OutBoost — голос ChatGPT по подписке приходит тише Gemini примерно на 13 дБ (замеры voicetest.go и живых разговоров:
// ChatGPT −27…−28 дБ, Gemini −12…−16,5 дБ): ×4,5 выравнивает их, пики держит ограничитель плеера.
func (s *ChatGPTSession) OutBoost() float64 { return 4.5 }

func NewChatGPTSession(parent context.Context, auth func(context.Context) (ChatGPTAuth, error), prof Profile, instructions string,
	client *http.Client, brain Brain, journal *Journal) *ChatGPTSession {
	ctx, cancel := context.WithCancel(parent)
	s := &ChatGPTSession{getAuth: auth, prof: prof, instructions: instructions, client: client, brain: brain,
		ctx: ctx, cancel: cancel, started: make(chan error, 1), Out: make(chan []int16, 400), gain: math.Pow(10, 15.0/20)}
	s.dialog.journal, s.dialog.lastActivity = journal, time.Now()
	s.dialog.route, s.dialog.model = "GPT-Live (подписка)", chatgptLiveModel
	go s.run()
	return s
}

func (s *ChatGPTSession) start(err error) { s.once.Do(func() { s.started <- err }) }

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *ChatGPTSession) run() {
	defer close(s.Out)
	defer s.players.Wait() // голос закрывается только когда его больше никто не пишет
	t0 := time.Now()
	if err := s.connect(); err != nil {
		s.start(err)
		s.cancel()
		return
	}
	log.Printf("GPT-Live (подписка): звонок открыт за %.2f с (голос %s)", time.Since(t0).Seconds(), chatgptVoice(s.prof.Voice))
	go s.feeder()
	<-s.ctx.Done()
}

func (s *ChatGPTSession) connect() error {
	t0 := time.Now()
	actx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	a, err := s.getAuth(actx)
	cancel()
	if err != nil {
		return err
	}
	s.auth = a
	tAuth := time.Since(t0)
	prep, warm := takeRTC(), true
	if prep == nil {
		warm = false
		if prep, err = prepareRTC(); err != nil {
			return err
		}
	}
	defer warmRTC() // следующий звонок — уже на готовом соединении
	tPrep := time.Since(t0) - tAuth
	pc := prep.pc
	s.pc, s.track, s.dc = pc, prep.track, prep.dc
	go func() { <-s.ctx.Done(); _ = pc.Close() }()
	prep.mu.Lock()
	prep.sink = func(msg string) { s.event([]byte(msg)) }
	prep.mu.Unlock()
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() == webrtc.RTPCodecTypeAudio && s.ctx.Err() == nil {
			s.players.Add(1)
			go func() { defer s.players.Done(); s.play(tr) }()
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			s.start(errors.New("звонок не соединился (WebRTC)"))
			s.cancel()
		}
	})
	t1 := time.Now()
	answer, err := s.createCall(prep.sdp)
	if err != nil {
		return err
	}
	tCall := time.Since(t1)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return fmt.Errorf("ответ OpenAI не подошёл: %w", err)
	}
	t2 := time.Now()
	select {
	case <-prep.opened:
	case <-time.After(10 * time.Second):
		return errors.New("звонок не соединился за 10 с — возможно, OpenAI не пропускает звук из этой сети")
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	how := "соединение с нуля"
	if warm {
		how = "готовое соединение"
	}
	log.Printf("GPT-Live (подписка): вход %.2f с, %s %.2f с, запрос звонка %.2f с, рукопожатие %.2f с",
		tAuth.Seconds(), how, tPrep.Seconds(), tCall.Seconds(), time.Since(t2).Seconds())
	s.start(nil)
	return nil
}

func (s *ChatGPTSession) createCall(sdp string) (string, error) {
	voice := chatgptVoice(s.prof.Voice)
	session := map[string]any{"model": chatgptLiveModel, "instructions": s.instructions + liveRules,
		"audio": map[string]any{"output": map[string]any{"voice": voice}}, "delegation": map[string]any{"type": "client"}}
	// Голос подписки слушает характер и память только из начального сообщения разработчика: одни instructions
	// он игнорирует и представляется «ChatGPT от OpenAI» (проверено звонками: с initial_items — «Я Джарвис»).
	session["initial_items"] = []any{map[string]any{"type": "message", "role": "developer",
		"content": []any{map[string]any{"type": "input_text", "text": s.instructions + liveRules}}}}
	body, _ := json.Marshal(map[string]any{"sdp": sdp, "session": session})
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCallURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.auth.Access)
	req.Header.Set("ChatGPT-Account-ID", s.auth.Account)
	req.Header.Set("OpenAI-Alpha", "quicksilver=v2")
	thread := chatgptChat.Thread() // один чат: все звонки — одна беседа
	if thread == "" {
		thread = newID()
	}
	req.Header.Set("session-id", thread)
	req.Header.Set("thread-id", thread)
	req.Header.Set("x-session-id", newID())
	req.Header.Set("originator", "codex_cli_rs")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("не подключиться к ChatGPT: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == 401:
		return "", errors.New("ChatGPT не принял вход (401) — войдите заново в «Подключениях»")
	case resp.StatusCode == 403:
		return "", errors.New("ChatGPT отказал (403): в подписке нет голоса или исчерпан лимит")
	case resp.StatusCode == 429:
		return "", errors.New("лимит голоса в подписке ChatGPT исчерпан (429) — попробуйте позже")
	case resp.StatusCode >= 300:
		return "", fmt.Errorf("ChatGPT ответил %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if u, err := url.Parse(loc); err == nil {
			log.Printf("GPT-Live (подписка): звонок %s", u.Path[strings.LastIndex(u.Path, "/")+1:])
		}
	}
	return string(data), nil
}

func (s *ChatGPTSession) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.dcMu.Lock()
	defer s.dcMu.Unlock()
	if s.dc == nil {
		return errors.New("канал не открыт")
	}
	return s.dc.SendText(string(b))
}

// event — событие голосовой модели из канала данных.
func (s *ChatGPTSession) event(raw []byte) {
	var ev map[string]any
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	typ := str(ev["type"])
	s.stat(func() { s.events[typ]++ })
	if chatgptDebug && typ != "turn.delta" {
		log.Printf("GPT-Live событие: %.400s", raw)
	}
	// текст реплик: сейчас приходит событиями *_transcript.added с item.text (по ~0,2 с речи);
	// старые *_transcript.delta тоже понимаем. Без этого Джарвис «не слышал» человека в звонке:
	// таймер тишины не сбрасывался, и разговор обрывался, пока модель думала
	itemText := func() string {
		if it, ok := ev["item"].(map[string]any); ok {
			return str(it["text"])
		}
		return str(ev["delta"])
	}
	switch typ {
	case "session.input_transcript.delta", "input_transcript.delta", "input_transcript.added", "session.input_transcript.added":
		s.onUser(itemText())
	case "session.output_transcript.delta", "output_transcript.delta", "output_transcript.added", "session.output_transcript.added":
		s.onBot(itemText())
	case "delegation.created", "session.delegation.created":
		item, _ := ev["item"].(map[string]any)
		if item == nil {
			item = ev
		}
		id := str(item["id"])
		var task strings.Builder
		if parts, ok := item["content"].([]any); ok {
			for _, p := range parts {
				if m, ok := p.(map[string]any); ok {
					task.WriteString(str(m["text"]))
				}
			}
		}
		if t := task.String(); id != "" && t != "" {
			s.working(+1)
			go s.delegate(id, t)
		}
	case "error":
		msg := errMessage(ev)
		log.Printf("GPT-Live (подписка): ошибка: %s", msg)
		if chatgptLimitText(msg) {
			chatgptLimitedUntil.Store(time.Now().Add(chatgptLimitRest).UnixNano())
			log.Printf("GPT-Live (подписка): кончился лимит голоса в подписке ChatGPT — час говорю через Gemini, потом попробую снова")
			s.FinishTurn("кончился лимит голоса в подписке ChatGPT — позовите ещё раз, ответит Gemini")
			s.cancel()
		}
	case "session.closed":
		s.cancel()
	}
}

// delegate — модель попросила подумать или что-то сделать: спрашиваем мозг, ответ — ей обратно.
func (s *ChatGPTSession) delegate(id, task string) {
	defer s.working(-1)
	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
	defer cancel()
	log.Printf("GPT-Live (подписка): задача бэкенду: %s", task)
	answer, err := s.brain(ctx, s.withHistory(task))
	if err != nil {
		log.Printf("GPT-Live (подписка): бэкенд: %v", err)
		answer = "Не получилось: " + shortErr(err)
	}
	for _, part := range chunkBytes(answer, 500) {
		_ = s.send(map[string]any{"type": "delegation.context.append", "delegation_item_id": id, "channel": "speakable",
			"content": []any{map[string]any{"type": "input_text", "text": part}}})
	}
}

// withHistory — голосовая модель передаёт бэкенду только последнюю фразу («ну в Москве посмотри»),
// поэтому добавляем ход разговора за последние полчаса: без него бэкенд не понимает, о чём речь.
func (s *ChatGPTSession) withHistory(task string) string {
	if chatgptChat.Enabled() { // общий чат: весь недавний разговор, а не только последние полчаса
		mem := chatgptChat.Transcript(4000)
		if mem == "" {
			return task
		}
		return "Разговор до этого (для контекста; что было раньше — ищи инструментом recall_conversation):\n" + mem +
			"\n\nЗадача от голосового ассистента — выполни её с учётом разговора выше: " + task
	}
	items := s.journal.Items(16)
	var lines []string
	for i := len(items) - 1; i >= 0; i-- {
		t := items[i]
		if time.Since(time.UnixMilli(int64(t.TS*1000))) > 30*time.Minute {
			continue
		}
		if t.Text != "" {
			lines = append(lines, "Человек: "+t.Text)
		}
		if t.Answer != "" {
			lines = append(lines, "Джарвис: "+t.Answer)
		}
	}
	if len(lines) == 0 {
		return task
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return "Разговор до этого (для контекста):\n" + strings.Join(lines, "\n") +
		"\n\nЗадача от голосового ассистента — выполни её с учётом разговора выше: " + task
}

// chunkBytes — нарезать текст на куски не длиннее n байт, не разрывая буквы.
func chunkBytes(text string, n int) []string {
	var out []string
	var cur strings.Builder
	for _, r := range text {
		if cur.Len()+len(string(r)) > n {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// play — голос модели: RTP → буфер против джиттера → Opus → 48 кГц.
func (s *ChatGPTSession) play(tr *webrtc.TrackRemote) {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(chatgptRate, 1))
	if err != nil {
		log.Printf("GPT-Live (подписка): декодер: %v", err)
		return
	}
	dropped := 0
	jb := newJitterBuf(dec, func(pcm []int16) {
		s.level.add(pcm, chatgptRate)
		if loud(pcm) {
			s.heardVoice()
			s.stat(func() { s.gotLoud++ })
		}
		select {
		case s.Out <- pcm:
		default:
			dropped++
		}
	})
	defer func() {
		jb.remember()
		if dropped > 0 {
			log.Printf("GPT-Live (подписка): плеер не успевал забирать голос — выброшено %d кусков", dropped)
		}
		if jb.lost > 0 {
			log.Printf("GPT-Live (подписка): потерянных пакетов голоса за звонок — %d, закрыты восстановлением", jb.lost)
		}
		if jb.under > 0 {
			log.Printf("GPT-Live (подписка): голос приходил рывками — %d провалов, всего %.0f мс; запас вырос до %d мс",
				jb.under, jb.underMs, jb.preroll*20)
		}
	}()
	lastPkt := time.Now()
	for {
		_ = tr.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
		pkt, _, err := tr.ReadRTP()
		if err != nil {
			var ne net.Error
			if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, os.ErrDeadlineExceeded) {
				jb.idle(time.Since(lastPkt)) // фраза кончилась раньше, чем набрался запас, — отдать, что есть
				if s.ctx.Err() != nil {
					return
				}
				continue
			}
			jb.flush()
			return
		}
		lastPkt = time.Now()
		jb.push(pkt)
	}
}

func (s *ChatGPTSession) Ready(timeout time.Duration) error {
	select {
	case err := <-s.started:
		return err
	case <-time.After(timeout):
		return errors.New("ChatGPT долго не отвечает")
	}
}

func (s *ChatGPTSession) Feed(pcm []int16) {
	s.pendingMu.Lock()
	s.pending = queueMic(s.pending, pcm)
	s.pendingMu.Unlock()
}

// feeder — микрофон: каждые 20 мс кадр Opus (тишина, если нового звука нет).
func (s *ChatGPTSession) feeder() {
	const frame = MicRate / 50
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: MicRate, Channels: 1})
	if err != nil {
		log.Printf("GPT-Live (подписка): кодер: %v", err)
		return
	}
	pcm := make([]int16, frame)
	out := make([]byte, 1500)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
		s.pendingMu.Lock()
		// звонок идёт строго в реальном времени, а к открытию накапливается 3–5 с звука (оклик + пока
		// соединялись): модель слышала бы всё с этим опозданием. Догоняем на паузах — выкидываем только
		// настоящие паузы (тишина ≥0,3 с подряд), речь и короткие промежутки в ней отправляем все
		for skipped := 0; skipped < 25 && quietRun(s.pending, frame, 15); skipped++ {
			s.pending = s.pending[frame:]
		}
		n := copy(pcm, s.pending)
		s.pending = s.pending[n:]
		s.pendingMu.Unlock()
		for i := range pcm {
			if i >= n {
				pcm[i] = 0
				continue
			}
			pcm[i] = int16(max(-32767, min(32767, float64(pcm[i])*s.gain)))
		}
		if n > 0 && loud(pcm[:n]) {
			s.stat(func() { s.sentLoud++ })
		}
		m, err := enc.EncodeInt16(pcm, out)
		if err != nil || m <= 0 {
			continue
		}
		_ = s.track.WriteSample(media.Sample{Data: append([]byte(nil), out[:m]...), Duration: 20 * time.Millisecond})
	}
}

// Say — произнести фразу (озвучка ответа из переписки, прослушивание голоса).
func (s *ChatGPTSession) Say(text string) error {
	var err error
	for _, part := range chunkBytes(text, 500) {
		if e := s.send(map[string]any{"type": "session.context.append", "channel": "speakable",
			"content": []any{map[string]any{"type": "input_text", "text": part}}}); e != nil {
			err = e
		}
	}
	return err
}

func (s *ChatGPTSession) Close() {
	s.stat(func() {
		log.Printf("GPT-Live (подписка): за звонок — речи модели отправлено %.1f с, её голоса получено %d кусков (%s), события: %v",
			float64(s.sentLoud)/50, s.gotLoud, s.level.String(), s.events)
	})
	if s.dc != nil {
		_ = s.send(map[string]any{"type": "session.close"})
		time.Sleep(200 * time.Millisecond)
	}
	s.cancel()
}

var chatgptDebug = os.Getenv("JARVIS_CHATGPT_DEBUG") != ""
