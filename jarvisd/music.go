package main

// Звук любого видео с YouTube: «Джарвис, включи с ютуба …». Поиск — по всему YouTube, звук — дорожка Opus
// (itag 251) прямой ссылкой. Разбор WebM и декодер Opus свои, на Go: ни yt-dlp, ни ffmpeg на колонке
// не нужны. Играет отдельным aplay поверх dmix, так что голос Джарвиса звучит поверх музыки, а музыка
// в это время приглушается. Умеет паузу, продолжение и перемотку: позиция берётся из меток времени WebM,
// а с нужного места скачивание начинается заново (поиск ближайшего кластера).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thesyncim/gopus"
)

const (
	ytVRVersion = "1.65.10"
	ytVRAgent   = "com.google.android.apps.youtube.vr.oculus/" + ytVRVersion + " (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip"
	musicRate   = OutRate // Opus и так 48 кГц: при 24 кГц терялись верха, а ALSA ещё и грубо пересчитывала обратно в 48
)

type Track struct {
	ID, Title, Author string
	Art               string `json:",omitempty"` // обложка (у Spotify)
}

// ytStream — дорожка: ссылка, с каким User-Agent её качать, размер и длительность.
type ytStream struct {
	url, ua string
	client  string // каким клиентом получена ссылка
	length  int64
	dur     float64   // секунды
	ready   time.Time // раньше этого YouTube не отдаёт звук: «показывает» рекламу перед видео

}

type Music struct {
	client        func() *http.Client
	volume        func() float64 // 0..1 с учётом приглушения
	aplay         string
	requestMu     sync.Mutex // один поиск/запуск, даже если его одновременно попросили два обработчика
	playMu        sync.Mutex // предыдущий процесс звука должен завершиться до следующего
	playback      func(context.Context, int, string, *ytStream, float64) error
	loadingCancel context.CancelFunc
	lastQuery     string
	lastStart     time.Time
	requestedAt   time.Time

	mu       sync.Mutex
	cancel   context.CancelFunc
	cur      Track
	st       *ytStream
	pos      float64 // где сейчас играет, секунды
	paused   bool
	pausedAt time.Time
	OnPlay   func()  // зазвучало (включили, продолжили, перемотали): другая музыка (Алиса) должна замолчать
	results  []Track // что ещё нашлось по последнему запросу — для «дальше»
	gen      int
	visitor  string // visitorData YouTube: с ним клиент VR отдаёт дорожку целиком, без проверки «не бот ли вы»
	visAt    time.Time
	js       ytJS // расшифровка ссылок клиента «телевизор»

	history     []Track
	queue       []Track   // «добавь в очередь»
	mix         []Track   // похожее на то, что играет, — когда очередь кончилась
	pre         preStream // следующий трек, подготовленный заранее
	prefetching bool

	Radio        func() bool  // после очереди играть похожее (nil — да)
	OnRelatedEnd func(string) // предложить продолжение выбранного видео, не включая без подтверждения

	CookieFile     string     // cookies.txt аккаунта YouTube — для 18+ и «вы не бот» (пусто — без входа)
	Account        *YTAccount // вход в YouTube кодом, как на телевизоре
	personal       bool
	nextSeed       string // для какого видео подобраны results (рекомендации YouTube под ним)
	personalSearch bool
	proposal       *mediaProposal
	progress       *playbackHistory
}

func NewMusic(client func() *http.Client, volume func() float64) *Music {
	return &Music{client: client, volume: volume, aplay: NewPlayer().aplay}
}

// Playing — звучит прямо сейчас.
func (m *Music) Playing() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

// Active — играет или стоит на паузе: команды «продолжи», «перемотай» относятся к музыке.
func (m *Music) Active() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil || m.paused
}

func clock(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Status — что играет, для модели и для ответа на «что играет».
func (m *Music) Status() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil && !m.paused {
		return ""
	}
	s := fmt.Sprintf("YouTube: «%s» — %s", m.cur.Title, m.cur.Author)
	if m.st != nil && m.st.dur > 0 {
		s += fmt.Sprintf(", %s из %s", clock(m.pos), clock(m.st.dur))
	}
	if m.paused {
		s += " (на паузе)"
	}
	return s
}

func (m *Music) halt() context.CancelFunc {
	c := m.cancel
	m.cancel = nil
	m.gen++
	return c
}

func (m *Music) Stop() bool {
	if m == nil {
		return false
	}
	m.saveProgress(false)
	m.mu.Lock()
	m.proposal = nil
	if m.loadingCancel != nil {
		m.loadingCancel()
	}
	c := m.halt()
	was := c != nil || m.paused
	m.paused = false
	m.mu.Unlock()
	if c != nil {
		c()
	}
	if was {
		log.Printf("музыка: остановлена")
	}
	return was
}

// Pause — остановить, запомнив место.
func (m *Music) Pause() bool {
	m.saveProgress(false)
	m.mu.Lock()
	c := m.halt()
	if c != nil {
		m.paused, m.pausedAt = true, time.Now()
	}
	pos := m.pos
	m.mu.Unlock()
	if c != nil {
		c()
		log.Printf("музыка: пауза на %s", clock(pos))
	}
	return c != nil
}

// Resume — продолжить с того же места.
func (m *Music) Resume() error {
	m.mu.Lock()
	paused, pos := m.paused, m.pos
	m.mu.Unlock()
	if !paused {
		return errors.New("музыка не на паузе")
	}
	return m.seekTo(pos)
}

// Seek — перемотать: delta секунд вперёд (минус — назад) или, если abs, на позицию delta.
func (m *Music) Seek(delta float64, abs bool) error {
	m.mu.Lock()
	if m.st == nil || (m.cancel == nil && !m.paused) {
		m.mu.Unlock()
		return errors.New("сейчас ничего не играет")
	}
	target := m.pos + delta
	if abs {
		target = delta
	}
	dur := m.st.dur
	if dur > 0 {
		target = min(target, dur-2)
	}
	target = max(0, target)
	if m.paused {
		m.pos = target
		m.mu.Unlock()
		m.saveProgress(false)
		return nil
	}
	m.mu.Unlock()
	return m.seekTo(target)
}

func (m *Music) seekTo(sec float64) error {
	m.mu.Lock()
	st, t := m.st, m.cur
	m.mu.Unlock()
	if st == nil {
		return errors.New("сейчас ничего не играет")
	}
	m.launch(t, st, sec)
	log.Printf("музыка: с %s", clock(sec))
	return nil
}

// Play — найти и включить. Возвращает, что включилось.
func (m *Music) Play(ctx context.Context, query string) (Track, error) {
	if recommendationRequest(query) {
		return Track{}, errors.New("подбор требует предложения ролика и явного подтверждения, а не буквального поиска")
	}
	m.requestMu.Lock()
	defer m.requestMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Track{}, err
	}
	m.mu.Lock()
	if m.lastQuery == normText(query) && time.Since(m.lastStart) < 3*time.Second && m.cancel != nil {
		t := m.cur
		m.mu.Unlock()
		return t, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	m.loadingCancel, m.requestedAt = cancel, time.Now()
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); m.loadingCancel = nil; m.mu.Unlock() }()
	var tracks []Track
	var err error
	personal := recommendationRequest(query)
	personalSearch := false
	if personal {
		tracks, err = m.recommendations(ctx)
		tracks = recommendationFilter(query, tracks)
		m.mu.Lock()
		played := map[string]bool{m.cur.ID: true}
		for _, tr := range m.history {
			played[tr.ID] = true
		}
		m.mu.Unlock()
		fresh := tracks[:0]
		for _, tr := range tracks {
			if !played[tr.ID] {
				fresh = append(fresh, tr)
			}
		}
		tracks = fresh
		if err == nil && len(tracks) == 0 {
			tracks, err = m.accountSearch(ctx, query)
			personalSearch = true
			fresh = tracks[:0]
			for _, tr := range tracks {
				if !played[tr.ID] {
					fresh = append(fresh, tr)
				}
			}
			tracks = fresh
		}
	} else {
		tracks, err = m.search(ctx, query)
	}
	if err != nil {
		return Track{}, err
	}
	if len(tracks) == 0 {
		if personal {
			return Track{}, errors.New("в рекомендациях и поиске с твоим входом пока не нашёл подходящего ролика для прослушивания")
		}
		return Track{}, fmt.Errorf("на YouTube ничего не нашлось по запросу «%s»", query)
	}
	var lastErr error
	for i, track := range tracks[:min(4, len(tracks))] {
		t, err := m.start(ctx, track)
		if err == nil {
			m.mu.Lock()
			m.results, m.lastQuery, m.lastStart, m.mix = tracks[i+1:], normText(query), time.Now(), nil
			m.personal = personal || listenScore(t) > 0
			if m.personal {
				m.results = nil
			}
			m.personalSearch = personalSearch
			m.mu.Unlock()
			return t, nil
		}
		if ctx.Err() != nil {
			return Track{}, ctx.Err()
		}
		lastErr = err
		log.Printf("YouTube: видео %s «%s»: %v — пробую следующий результат", track.ID, track.Title, err)
	}
	return Track{}, lastErr
}

// Next — следующее: из очереди, похожее (радио) или следующий результат последнего поиска.
func (m *Music) Next(ctx context.Context) (Track, error) {
	m.requestMu.Lock()
	defer m.requestMu.Unlock()
	t, ok := m.popNext(ctx)
	if !ok {
		return Track{}, errors.New("дальше ничего нет")
	}
	return m.start(ctx, t)
}

func (m *Music) start(ctx context.Context, t Track) (Track, error) {
	t0 := time.Now()
	st, info, ok := m.takePrefetched(t.ID)
	if !ok {
		var err error
		if st, info, err = m.audioURL(ctx, t.ID); err != nil {
			return t, err
		}
	}
	if t.Title == "" {
		t.Title, t.Author = info.Title, info.Author
	}
	if err := ctx.Err(); err != nil {
		return t, err
	}
	m.launch(t, st, 0)
	m.remember(t)
	how := "ссылка за " + strconv.FormatFloat(time.Since(t0).Seconds(), 'f', 1, 64) + " с"
	if ok {
		how = "подготовлен заранее"
	}
	if w := time.Until(st.ready); w > 300*time.Millisecond {
		how += ", у ссылки реклама " + strconv.FormatFloat(w.Seconds(), 'f', 1, 64) + " с"
	}
	log.Printf("музыка: играет «%s» — %s (%s; %s)", t.Title, t.Author, clock(st.dur), how)
	m.prefetchSoon()
	return t, nil
}

// launch — заменить то, что играет, на дорожку st с позиции sec.
func (m *Music) launch(t Track, st *ytStream, sec float64) {
	m.saveProgress(false)
	pctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	old := m.halt()
	m.cancel, m.cur, m.st, m.pos, m.paused = cancel, t, st, sec, false
	gen := m.gen
	onPlay := m.OnPlay
	m.mu.Unlock()
	if old != nil {
		old()
	}
	if onPlay != nil {
		go onPlay()
	}
	m.saveProgress(false)
	go func() {
		m.playMu.Lock()
		defer m.playMu.Unlock()
		if pctx.Err() != nil {
			cancel()
			return
		}
		run := m.playback
		if run == nil {
			run = m.play
		}
		err := run(pctx, gen, t.ID, st, sec)
		natural := err == nil && pctx.Err() == nil
		if err != nil && pctx.Err() == nil {
			log.Printf("музыка: %v", err)
		}
		m.mu.Lock()
		if m.gen == gen {
			m.cancel = nil
		}
		pos := m.pos
		m.mu.Unlock()
		cancel()
		if natural && m.playback == nil {
			go m.ended(gen, pos, st.dur)
		}
	}()
}

func (m *Music) post(ctx context.Context, url string, body any, ua string, hdr ...string) (map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("YouTube ответил %d", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("YouTube ответил %d", resp.StatusCode)
	}
	return out, nil
}

// search — обычный поиск YouTube по видео, без ограничения музыкальным каталогом.
func (m *Music) search(ctx context.Context, query string) ([]Track, error) {
	// как поиск на сайте, без фильтра: с фильтром «только видео» YouTube не исправляет опечатки
	// и на неточные запросы («гимн twitch от goc hornet») не находит ничего. Каналы и плейлисты
	// youtubeResults и так пропускает; фильтр — запасной вариант.
	tracks, err := m.searchWith(ctx, query, "")
	if err == nil && len(tracks) == 0 {
		tracks, err = m.searchWith(ctx, query, "EgIQAQ==")
	}
	return tracks, err
}

func (m *Music) searchWith(ctx context.Context, query, params string) ([]Track, error) {
	body := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": "2.20261112.01.00", "hl": "ru", "gl": "RU"}},
		"query": query}
	if params != "" {
		body["params"] = params
	}
	out, err := m.post(ctx, "https://www.youtube.com/youtubei/v1/search?prettyPrint=false", body,
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0 Safari/537.36")
	if err != nil {
		return nil, err
	}
	if rc, ok := out["responseContext"].(map[string]any); ok && str(rc["visitorData"]) != "" {
		m.mu.Lock()
		m.visitor, m.visAt = str(rc["visitorData"]), time.Now()
		m.mu.Unlock()
	}
	return youtubeResults(out), nil
}

func youtubeText(v any) string {
	o, _ := v.(map[string]any)
	if s := str(o["simpleText"]); s != "" {
		return s
	}
	var b strings.Builder
	runs, _ := o["runs"].([]any)
	for _, r := range runs {
		run, _ := r.(map[string]any)
		b.WriteString(str(run["text"]))
	}
	return b.String()
}

func youtubeResults(out map[string]any) []Track {
	contents, _ := out["contents"].(map[string]any)
	column, _ := contents["twoColumnSearchResultsRenderer"].(map[string]any)
	primary, _ := column["primaryContents"].(map[string]any)
	section, _ := primary["sectionListRenderer"].(map[string]any)
	var tracks []Track
	seen := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		if len(tracks) >= 12 {
			return
		}
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				visit(item)
			}
		case map[string]any:
			if video, ok := v["videoRenderer"].(map[string]any); ok {
				t := Track{ID: str(video["videoId"]), Title: youtubeText(video["title"]), Author: youtubeText(video["ownerText"])}
				if t.Author == "" {
					t.Author = youtubeText(video["longBylineText"])
				}
				if t.ID != "" && t.Title != "" && !seen[t.ID] {
					seen[t.ID] = true
					tracks = append(tracks, t)
				}
				return
			}
			// Только органическая выдача и в её порядке. Рекламные блоки, каналы и плейлисты пропускаем.
			for _, key := range []string{"contents", "itemSectionRenderer", "shelfRenderer", "content", "verticalListRenderer", "items", "richItemRenderer"} {
				if child, ok := v[key]; ok {
					visit(child)
				}
			}
		}
	}
	visit(section["contents"])
	return tracks
}

// walk — обойти JSON и показать каждый объект под его ключом.
func walk(v any, fn func(key string, obj map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if o, ok := c.(map[string]any); ok {
				fn(k, o)
			}
			walk(c, fn)
		}
	case []any:
		for _, c := range x {
			walk(c, fn)
		}
	}
}

// ytClient — как представиться YouTube. Для части видео один клиент просит вход, другой — нет,
// поэтому пробуем по очереди.
type ytClient struct {
	ctx map[string]any
	ua  string
}

var ytClients = []ytClient{
	// VR отдаёт дорожку целиком (без особого токена), но только с visitorData; iOS — запасной: его ссылки
	// обрываются примерно на первом мегабайте (минута песни)
	{map[string]any{"clientName": "ANDROID_VR", "clientVersion": ytVRVersion, "deviceMake": "Oculus", "deviceModel": "Quest 3",
		"androidSdkVersion": 32, "osName": "Android", "osVersion": "12L"}, ytVRAgent},
	{map[string]any{"clientName": "IOS", "clientVersion": "20.10.4", "deviceMake": "Apple", "deviceModel": "iPhone16,2",
		"osName": "iPhone", "osVersion": "18.3.2.22D82"},
		"com.google.ios.youtube/20.10.4 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)"},
}

const (
	ytTVVersion = "5.20260707"
	ytTVAgent   = "Mozilla/5.0 (SMART-TV; Linux; Tizen 2.4.0) AppleWebKit/538.1 (KHTML, like Gecko) Version/2.4.0 TV Safari/538.1"
)

// audioURL — ссылка на дорожку Opus, размер и длительность. Сначала клиент «телевизор»: он отдаёт песню
// целиком; если с ним не вышло — VR и iOS (их ссылки без особого токена обрываются примерно на минуте).
func (m *Music) audioURL(ctx context.Context, id string) (*ytStream, Track, error) {
	m.mu.Lock()
	vd, fresh := m.visitor, time.Since(m.visAt) < time.Hour
	m.mu.Unlock()
	if vd == "" || !fresh { // visitorData выдаёт любой поиск
		_, _ = m.search(ctx, "музыка")
		m.mu.Lock()
		vd = m.visitor
		m.mu.Unlock()
	}
	st, info, err := m.tvURL(ctx, id, vd, nil)
	if err != nil && ytNeedsLogin(err) && m.Account != nil {
		// вход кодом есть — сразу как вошедший «телевизор»: так играют и 18+, и «вы не бот»
		if tok, terr := m.Account.Token(ctx); terr == nil && tok != "" {
			if st2, info2, err2 := m.tvURL(ctx, id, "", []string{"Authorization", "Bearer " + tok}); err2 == nil {
				log.Printf("музыка: YouTube просит вход — играю с аккаунтом")
				return st2, info2, nil
			} else {
				log.Printf("музыка: с аккаунтом YouTube: %v", err2)
			}
		} else if terr != nil {
			log.Printf("музыка: вход в YouTube: %v", terr)
		}
	}
	if err == nil {
		return st, info, nil
	}
	if ytNeedsLogin(err) {
		// 18+ без входа: встроенный плеер многие такие видео отдаёт
		if st, info2, err2 := m.embedURL(ctx, id, vd); err2 == nil {
			log.Printf("музыка: «телевизор» просит вход — играю через встроенный плеер")
			return st, info2, nil
		} else {
			log.Printf("музыка: встроенный плеер: %v", err2)
		}
		if c := loadYTCookies(m.CookieFile); c != nil {
			log.Printf("музыка: YouTube просит вход (%v) — пробую с аккаунтом", err)
			if st, info2, err2 := m.tvURL(ctx, id, "", c.headers()); err2 == nil {
				return st, info2, nil
			} else {
				err = err2
			}
		} else {
			log.Printf("музыка: YouTube просит вход (%v), а куки аккаунта нет", err)
		}
	}
	log.Printf("музыка: клиент «телевизор»: %v — пробую запасные (обрываются на минуте)", err)
	return m.fallbackURL(ctx, id, vd)
}

// tvURL — дорожка через клиент TVHTML5 (Samsung Tizen) с расшифровкой ссылки скриптом плеера.
func (m *Music) tvURL(ctx context.Context, id, vd string, auth []string) (*ytStream, Track, error) {
	info := Track{ID: id}
	sts, err := m.js.prepare(ctx, m.client())
	if err != nil {
		return nil, info, err
	}
	client := map[string]any{"clientName": "TVHTML5", "clientVersion": ytTVVersion, "deviceMake": "Samsung", "deviceModel": "UKS9800",
		"userAgent": ytTVAgent, "osName": "Tizen", "osVersion": "2.4.0", "hl": "ru", "gl": "RU"}
	hdr := []string{"X-YouTube-Client-Name", "7", "X-YouTube-Client-Version", ytTVVersion, "Origin", "https://www.youtube.com"}
	if vd != "" {
		client["visitorData"] = vd
		hdr = append(hdr, "X-Goog-Visitor-Id", vd)
	}
	hdr = append(hdr, auth...)
	body := map[string]any{"context": map[string]any{"client": client}, "videoId": id, "contentCheckOk": true, "racyCheckOk": true,
		"playbackContext": map[string]any{"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS", "signatureTimestamp": sts}}}
	t0 := time.Now()
	out, err := m.post(ctx, "https://www.youtube.com/youtubei/v1/player?prettyPrint=false", body, ytTVAgent, hdr...)
	if err != nil {
		return nil, info, err
	}
	if d, ok := out["videoDetails"].(map[string]any); ok {
		info = Track{ID: id, Title: str(d["title"]), Author: str(d["author"])}
	}
	if ps, ok := out["playabilityStatus"].(map[string]any); ok && str(ps["status"]) != "OK" {
		return nil, info, fmt.Errorf("%s %s", str(ps["status"]), str(ps["reason"]))
	}
	sd, _ := out["streamingData"].(map[string]any)
	fm, _ := sd["adaptiveFormats"].([]any)
	if st := m.pickSound(fm, func(mf map[string]any) (string, error) {
		return m.tvLink(mf)
	}, t0.Add(ytAdWait(out)), "TVHTML5", ytTVAgent); st != nil {
		return st, info, nil
	}
	return nil, info, errors.New("нет звуковой дорожки Opus")
}

// pickSound — дорожка Opus (251, 250, 249). Видео без Opus (бывает, YouTube отдаёт только AAC)
// пропускаем: Play возьмёт следующий результат поиска.
func (m *Music) pickSound(fm []any, link func(map[string]any) (string, error), ready time.Time, client, ua string) *ytStream {
	for _, want := range []float64{251, 250, 249} {
		cands := soundCandidates(fm, want)
		if len(cands) > 1 {
			if at, ok := cands[0]["audioTrack"].(map[string]any); ok {
				log.Printf("музыка: озвучек у видео несколько — беру «%s»", str(at["displayName"]))
			}
		}
		for _, mf := range cands {
			link0, err := link(mf)
			if err != nil {
				log.Printf("музыка: ссылка itag %v: %v", want, err)
				continue
			}
			st := &ytStream{url: link0, ua: ua, client: client, ready: ready}
			st.length, _ = strconv.ParseInt(str(mf["contentLength"]), 10, 64)
			ms, _ := strconv.ParseFloat(str(mf["approxDurationMs"]), 64)
			st.dur = ms / 1000
			return st
		}
	}
	return nil
}

// soundCandidates — дорожки с этим itag, лучшая первой. У видео с несколькими озвучками одинаковых itag
// несколько (английская, русская, дубляж ИИ…): берём основную (audioIsDefault — как на сайте), затем
// оригинал, затем русскую; вариант со сжатием динамики (isDrc) — только если другого нет.
func soundCandidates(fm []any, itag float64) []map[string]any {
	var out []map[string]any
	for _, f := range fm {
		if mf, ok := f.(map[string]any); ok && mf["itag"] == itag {
			out = append(out, mf)
		}
	}
	score := func(mf map[string]any) int {
		s := 0
		if at, ok := mf["audioTrack"].(map[string]any); ok {
			if def, _ := at["audioIsDefault"].(bool); def {
				s += 8
			}
			name := strings.ToLower(str(at["displayName"]))
			if strings.Contains(name, "original") || strings.Contains(name, "оригинал") {
				s += 4
			}
			if strings.HasPrefix(str(at["id"]), "ru") {
				s += 2
			}
		} else {
			s += 8 // дорожка одна — она и основная
		}
		if drc, _ := mf["isDrc"].(bool); !drc {
			s++
		}
		return s
	}
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out
}

// tvLink — собрать ссылку: расшифровать подпись (signatureCipher) и параметр n.
func (m *Music) tvLink(f map[string]any) (string, error) {
	raw, sig, sp := str(f["url"]), "", "signature"
	if c := str(f["signatureCipher"]); c != "" {
		q, err := url.ParseQuery(c)
		if err != nil {
			return "", err
		}
		raw, sig = q.Get("url"), q.Get("s")
		if v := q.Get("sp"); v != "" {
			sp = v
		}
	}
	u, err := url.Parse(raw)
	if raw == "" || err != nil {
		return "", errors.New("нет ссылки на дорожку")
	}
	q := u.Query()
	n := q.Get("n")
	if sig == "" && n == "" {
		return raw, nil
	}
	s2, n2, err := m.js.decipher(sig, n)
	if err != nil {
		return "", err
	}
	if sig != "" {
		q.Set(sp, s2)
	}
	if n != "" {
		q.Set("n", n2)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ytAdWait — сколько YouTube «показывает» рекламу перед видео: раньше звук не отдаст (403).
func ytAdWait(pr map[string]any) time.Duration {
	var total float64
	add := func(v any) {
		walk(v, func(k string, r map[string]any) {
			if k != "instreamVideoAdRenderer" {
				return
			}
			if ms, ok := r["skipOffsetMilliseconds"].(float64); ok {
				total += ms / 1000
				return
			}
			if q, err := url.ParseQuery(str(r["playerVars"])); err == nil {
				if l, err := strconv.ParseFloat(q.Get("length_seconds"), 64); err == nil {
					total += l
				}
			}
		})
	}
	if ps, ok := pr["adPlacements"].([]any); ok {
		for _, p := range ps {
			var kind string
			walk(p, func(k string, o map[string]any) {
				if k == "adPlacementConfig" {
					kind = str(o["kind"])
				}
			})
			if kind == "AD_PLACEMENT_KIND_START" {
				add(p)
			}
		}
	}
	if ss, ok := pr["adSlots"].([]any); ok {
		for _, s := range ss {
			var trig string
			walk(s, func(k string, o map[string]any) {
				if k == "adSlotMetadata" {
					trig = str(o["triggerEvent"])
				}
			})
			if trig == "SLOT_TRIGGER_EVENT_BEFORE_CONTENT" {
				add(s)
			}
		}
	}
	return time.Duration(total * float64(time.Second))
}

func (m *Music) fallbackURL(ctx context.Context, id, vd string) (*ytStream, Track, error) {
	var lastErr error
	info := Track{ID: id}
	for _, c := range ytClients {
		client := map[string]any{"hl": "ru", "gl": "RU"}
		for k, v := range c.ctx {
			client[k] = v
		}
		if vd != "" {
			client["visitorData"] = vd
		}
		body := map[string]any{"context": map[string]any{"client": client}, "videoId": id, "contentCheckOk": true, "racyCheckOk": true}
		out, err := m.post(ctx, "https://www.youtube.com/youtubei/v1/player?prettyPrint=false", body, c.ua)
		if err != nil {
			lastErr = err
			continue
		}
		if d, ok := out["videoDetails"].(map[string]any); ok {
			info = Track{ID: id, Title: str(d["title"]), Author: str(d["author"])}
		}
		if ps, ok := out["playabilityStatus"].(map[string]any); ok && str(ps["status"]) != "OK" {
			lastErr = fmt.Errorf("YouTube не даёт играть это видео: %s", str(ps["reason"]))
			log.Printf("музыка: %s: %s %s", str(c.ctx["clientName"]), str(ps["status"]), str(ps["reason"]))
			continue
		}
		sd, _ := out["streamingData"].(map[string]any)
		fm, _ := sd["adaptiveFormats"].([]any)
		if st := m.pickSound(fm, func(mf map[string]any) (string, error) {
			return str(mf["url"]), nil
		}, time.Time{}, str(c.ctx["clientName"]), c.ua); st != nil {
			return st, info, nil
		}
		lastErr = errors.New("у этого видео нет звуковой дорожки Opus")
	}
	return nil, info, lastErr
}

// ytReader — скачивание кусками: большими кусками YouTube не отдаёт (403), начинаем с 512 КБ и при отказе
// уменьшаем вдвое, до 64 КБ. Если отказывает и на 64 КБ — ссылка устарела (без особого токена её хватает
// примерно на мегабайт): берём новую и продолжаем с того же байта.
type ytReader struct {
	ctx     context.Context
	client  *http.Client
	url     string
	ua      string
	chunk   int64
	pos     int64
	body    io.ReadCloser
	eof     bool
	refresh func() (*ytStream, error)
	renewed int
	fails   int       // сетевых сбоев подряд
	main    *ytStream // пока играет начало с запасной ссылки: основная, на которую перейти после «рекламы»
}

// toMain — перейти с запасной ссылки (начало без рекламы) на основную, когда YouTube её уже отдаёт.
func (r *ytReader) toMain(force bool) error {
	if r.main == nil || (!force && time.Now().Before(r.main.ready)) {
		return nil
	}
	if err := sleepCtx(r.ctx, time.Until(r.main.ready)); err != nil {
		return err
	}
	r.url, r.ua, r.chunk, r.main = r.main.url, r.main.ua, 0, nil
	return nil
}

func (r *ytReader) Close() error {
	if r.body != nil {
		err := r.body.Close()
		r.body = nil
		return err
	}
	return nil
}

func (r *ytReader) Read(p []byte) (int, error) {
	for {
		if r.body == nil {
			if r.eof {
				return 0, io.EOF
			}
			if err := r.toMain(false); err != nil {
				return 0, err
			}
			req, _ := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
			req.Header.Set("User-Agent", r.ua)
			if r.chunk == 0 {
				r.chunk = 512 << 10
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.pos, r.pos+r.chunk-1))
			resp, err := r.client.Do(req)
			if err != nil {
				if r.retry(err) {
					continue
				}
				return 0, err
			}
			if resp.StatusCode == 416 {
				resp.Body.Close()
				return 0, io.EOF
			}
			if resp.StatusCode == 403 {
				resp.Body.Close()
				if r.main != nil { // запасная ссылка кончилась раньше «рекламы» — ждём основную
					if err := r.toMain(true); err != nil {
						return 0, err
					}
					continue
				}
				if r.chunk > 64<<10 {
					r.chunk /= 2
					continue
				}
				if r.refresh != nil && r.renewed < 30 {
					st, err := r.refresh()
					if err == nil {
						r.url, r.ua, r.chunk = st.url, st.ua, 0
						r.renewed++
						if err := sleepCtx(r.ctx, time.Until(st.ready)); err != nil {
							return 0, err
						}
						continue
					}
				}
				return 0, errors.New("YouTube не отдал звук (403)")
			}
			if resp.StatusCode != 206 && resp.StatusCode != 200 {
				resp.Body.Close()
				return 0, fmt.Errorf("YouTube не отдал звук (%d)", resp.StatusCode)
			}
			if cr := resp.Header.Get("Content-Range"); cr != "" {
				var a, b, total int64
				if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &a, &b, &total); err == nil && b+1 >= total {
					r.eof = true
				}
			} else {
				r.eof = true
			}
			r.body = resp.Body
		}
		n, err := r.body.Read(p)
		r.pos += int64(n)
		if err == io.EOF {
			r.body.Close()
			r.body = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil && r.ctx.Err() == nil { // связь оборвалась посреди куска — докачиваем с этого места
			r.body.Close()
			r.body = nil
			if n > 0 {
				return n, nil
			}
			if r.retry(err) {
				continue
			}
		}
		if err == nil {
			r.fails = 0
		}
		return n, err
	}
}

// retry — сеть до YouTube споткнулась (таймаут TLS, обрыв): ждём и пробуем ещё, до 4 раз подряд.
func (r *ytReader) retry(err error) bool {
	if r.ctx.Err() != nil || r.fails >= 4 {
		return false
	}
	r.fails++
	log.Printf("музыка: связь с YouTube споткнулась (%s) — повтор %d из 4", shortErr(err), r.fails)
	return sleepCtx(r.ctx, time.Duration(r.fails)*time.Second) == nil
}

// webm — минимальный разбор Matroska: из кластеров достаём кадры дорожки (SimpleBlock/Block) и их время.
type webm struct {
	r         *bufio.Reader
	clusterMs int64
}

func (w *webm) vint(keepMarker bool) (uint64, int, error) {
	b, err := w.r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	n := 1
	for mask := byte(0x80); n <= 8 && b&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 {
		return 0, 0, errors.New("битый WebM")
	}
	v := uint64(b)
	if !keepMarker {
		v &= uint64(0xFF >> n)
	}
	for i := 1; i < n; i++ {
		c, err := w.r.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		v = v<<8 | uint64(c)
	}
	return v, n, nil
}

// resync — найти начало ближайшего кластера (после перемотки читаем с произвольного байта).
func (w *webm) resync() error {
	var win uint32
	for i := 0; i < 8<<20; i++ {
		b, err := w.r.ReadByte()
		if err != nil {
			return err
		}
		win = win<<8 | uint32(b)
		if win == 0x1F43B675 {
			_, _, err := w.vint(false) // размер кластера
			return err
		}
	}
	return errors.New("в WebM не нашлось начала кластера")
}

// next — следующий кадр Opus и его время от начала дорожки, мс.
func (w *webm) next() ([]byte, int64, error) {
	for {
		id, _, err := w.vint(true)
		if err != nil {
			return nil, 0, err
		}
		size, n, err := w.vint(false)
		if err != nil {
			return nil, 0, err
		}
		unknown := size == (uint64(1)<<(7*n))-1
		switch id {
		case 0x18538067, 0x1F43B675, 0xA0: // Segment, Cluster, BlockGroup — заходим внутрь
			continue
		case 0xE7: // время кластера, мс
			if size > 8 {
				return nil, 0, errors.New("битое время кластера")
			}
			var v int64
			for i := uint64(0); i < size; i++ {
				c, err := w.r.ReadByte()
				if err != nil {
					return nil, 0, err
				}
				v = v<<8 | int64(c)
			}
			w.clusterMs = v
		case 0xA3, 0xA1: // SimpleBlock, Block
			if unknown || size < 4 || size > 1<<20 {
				return nil, 0, errors.New("битый блок WebM")
			}
			data := make([]byte, size)
			if _, err := io.ReadFull(w.r, data); err != nil {
				return nil, 0, err
			}
			// номер дорожки (vint), время (2 байта), флаги (1 байт); кружево у YouTube не используется
			k := 1
			for mask := byte(0x80); k <= 8 && data[0]&mask == 0; mask >>= 1 {
				k++
			}
			if k+3 > len(data) || data[k+2]&0x06 != 0 {
				continue
			}
			rel := int64(int16(uint16(data[k])<<8 | uint16(data[k+1])))
			return data[k+3:], w.clusterMs + rel, nil
		default:
			if unknown {
				return nil, 0, errors.New("элемент WebM неизвестной длины")
			}
			if _, err := w.r.Discard(int(size)); err != nil {
				return nil, 0, err
			}
		}
	}
}

// openAudio — открыть дорожку чуть раньше нужного места: Opus нужен короткий разгон,
// а оценка байта по длительности неточна из-за переменного битрейта.
func (m *Music) openAudio(ctx context.Context, id string, st *ytStream, startSec float64) (*ytReader, *webm, error) {
	var startByte int64
	if startSec > 0.5 && st.dur > 0 && st.length > 0 {
		startByte = max(0, int64(startSec/st.dur*float64(st.length))-(128<<10))
		startByte = max(0, min(startByte, st.length-(64<<10)))
	}
	src := &ytReader{ctx: ctx, client: m.client(), url: st.url, ua: st.ua, pos: startByte, refresh: func() (*ytStream, error) {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ns, _, err := m.audioURL(rctx, id)
		if err == nil {
			log.Printf("музыка: ссылка YouTube обновлена")
		}
		return ns, err
	}}
	if wait := time.Until(st.ready); wait > time.Second && startByte < 256<<10 {
		// YouTube не отдаёт звук «телевизору», пока идёт реклама, а VR рекламу не показывает, но отдаёт только
		// начало файла. Файл тот же (та же длина) — играем начало по ссылке VR, потом незаметно переходим.
		t0 := time.Now()
		hctx, hcancel := context.WithTimeout(ctx, 3*time.Second)
		m.mu.Lock()
		vd := m.visitor
		m.mu.Unlock()
		head, _, err := m.fallbackURL(hctx, id, vd)
		hcancel()
		if err == nil && head.length == st.length {
			src.url, src.ua, src.main = head.url, head.ua, st
			log.Printf("музыка: рекламу (%.1f с) не ждём — начало с %s (%.2f с)", wait.Seconds(), head.client, time.Since(t0).Seconds())
		} else if err == nil {
			log.Printf("музыка: у %s другой файл — жду рекламу %.1f с", head.client, wait.Seconds())
		}
	}
	if src.main == nil {
		if err := sleepCtx(ctx, time.Until(st.ready)); err != nil {
			return nil, nil, err
		}
	}
	w := &webm{r: bufio.NewReaderSize(src, 64<<10)}
	if startByte > 0 {
		if err := w.resync(); err != nil {
			_ = src.Close()
			return nil, nil, err
		}
	}
	return src, w, nil
}

func (m *Music) play(ctx context.Context, gen int, id string, st *ytStream, startSec float64) error {
	return m.playOpus(ctx, gen, id, st, startSec)
}

// playOpus — плейбэк Opus/WebM (существующий код).
func (m *Music) playOpus(ctx context.Context, gen int, id string, st *ytStream, startSec float64) error {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(musicRate, 1))
	if err != nil {
		return err
	}
	src, w, err := m.openAudio(ctx, id, st, startSec)
	if err != nil {
		return err
	}
	defer src.Close()
	frames := &musicFrames{src: src, w: w, target: int64(startSec * 1000)}
	cmd := exec.Command(m.aplay, "-q", "-D", "default", "-t", "raw", "-f", "S16_LE", "-r", itoa(musicRate), "-c", "1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = in.Close()
		if ctx.Err() != nil {
			_ = cmd.Process.Signal(syscall.SIGINT)
		}
		waitOrInt(cmd, 3*time.Second)
	}()
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Signal(syscall.SIGINT) })
	defer stop()
	pcm := make([]int16, musicRate/8)
	out := make([]byte, 0, len(pcm)*2)
	gain := 0.0
	bad := 0
	for ctx.Err() == nil {
		frame, ms, err := frames.next()
		if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			// после перемотки случайные байты могли сойти за начало кластера — ищем следующий
			if bad++; bad > 5 || w.resync() != nil {
				return err
			}
			continue
		}
		n, err := dec.DecodeInt16(frame, pcm)
		if err != nil || n <= 0 {
			continue
		}
		if ms < frames.target {
			continue // разогрев декодера до запрошенного места, в динамик не отправляем
		}
		m.mu.Lock()
		if m.gen == gen {
			m.pos = float64(ms) / 1000
		}
		m.mu.Unlock()
		target := m.volume()
		out = out[:0]
		for _, s := range pcm[:n] {
			// приглушение быстрое (~20 мс), возврат плавный (~1 с): музыка не «выпрыгивает» после ответа
			if target < gain {
				gain += (target - gain) * (0.002 * 24000 / musicRate)
			} else {
				gain += (target - gain) * (0.00015 * 24000 / musicRate)
			}
			out = binary.LittleEndian.AppendUint16(out, uint16(int16(float64(s)*gain)))
		}
		if _, err := in.Write(out); err != nil {
			return nil
		}
	}
	return nil
}

// musicFrames — кадры перед точкой перемотки тоже декодируются, но не звучат.
// Если оценка байта перескочила цель, читаем с начала, чтобы не пропускать запрошенное место.
type musicFrames struct {
	src     *ytReader
	w       *webm
	target  int64
	checked bool
}

func (f *musicFrames) next() ([]byte, int64, error) {
	frame, ms, err := f.w.next()
	if err == nil && !f.checked {
		f.checked = true
		if ms > f.target && f.target > 0 {
			_ = f.src.Close()
			f.src.pos, f.src.eof = 0, false
			f.w.r.Reset(f.src)
			f.w.clusterMs = 0
			return f.w.next()
		}
	}
	return frame, ms, err
}

// musicCommand — управление тем, что играет с YouTube: что за трек, пауза, продолжить, следующий, перемотка.
// action: what | pause | resume | stop | next | seek (secs — на сколько, abs — на позицию).
func musicCommand(text string) (action string, secs float64, abs bool, ok bool) {
	t := strings.ToLower(strings.TrimSpace(strings.Trim(text, " ,.!?")))
	t = strings.NewReplacer("ё", "е", ",", " ", ".", " ", "!", " ", "?", " ").Replace(t)
	words := strings.Fields(t)
	if len(words) > 0 && nameLike(words[0]) {
		words[0] = "джарвис"
	}
	t = leadRe.ReplaceAllString(strings.Join(words, " "), "")
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "пожалуйста "), " пожалуйста"))
	// Проверяем начало команды: «расскажи, как перемотать фильм» не управляет музыкой.
	if !musicCommandStart.MatchString(t) {
		return "", 0, false, false
	}
	t = " " + t + " "
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("что играет", "что сейчас играет", "что за песн", "что за трек", "что это за песн", "кто поет", "кто исполня",
		"как называется", "что за музык", "кто это поет"):
		return "what", 0, false, true
	case has(" в начало", " сначала", " с начала"):
		return "seek", 0, true, true
	case has("перемот", "промот", "мотни", "вперед на", "назад на", "отмотай"):
		if mark := seekClock.FindStringSubmatch(t); mark != nil {
			mins, _ := strconv.Atoi(mark[1])
			seconds, _ := strconv.Atoi(mark[2])
			return "seek", float64(mins*60 + seconds), true, true
		}
		secs = 30
		if amounts := seekAmount.FindAllStringSubmatch(t, -1); len(amounts) > 0 {
			secs = 0
			for _, a := range amounts {
				secs += parseAmount(a[1], a[2])
			}
		}
		abs = has("на отметку", "на позицию")
		if has("назад", "отмотай") {
			secs = -secs
		}
		return "seek", secs, abs, true
	case has("похож"):
		return "radio", 0, false, true
	case has("продолж", "сними с паузы", "играй дальше", "сыграй дальше", "сними паузу", "сними с пауз"):
		return "resume", 0, false, true
	case has("пауз", "останови музыку", "приостанов"):
		return "pause", 0, false, true
	case has("следующ", "другую песн", "другой трек", "переключи", " дальше "):
		return "next", 0, false, true
	case has("выключи музыку", "выключи песню", "выруби музыку"):
		return "stop", 0, false, true
	}
	return "", 0, false, false
}

var (
	musicCommandStart = regexp.MustCompile(`^(?:что (?:сейчас )?играет|что (?:это )?за (?:песн|трек|музык)|кто (?:это )?поет|кто исполня|как называется (?:эта |этот )?(?:песн|трек|композиц)|(?:включи |играй |поставь |начни )?(?:сначала|с начала|в начало)|перемот|промот|мотни|вперед на|назад на|отмотай|продолж|сними (?:с )?пауз|играй дальше|сыграй дальше|(?:поставь |включи )?на паузу|пауза|останови музыку|приостанов|следующ|(?:включи |поставь )?следующ|другую песн|другой трек|переключи|дальше|выключи музык|выключи песн|выруби музык|(?:включи |поставь |давай |играй )?(?:что-нибудь |что-то )?похож)`)
	seekClock         = regexp.MustCompile(`(?:на |отметку |позицию )(\d+):([0-5]\d)`)
	seekAmount        = regexp.MustCompile(`(\d+|полминуты|полторы|пятьдесят|пятнадцать|тридцать|двадцать|сорок|десять|пару|пять|четыре|три|два|две|одну|один|минуту)\s*(секунд\S*|сек|минут\S*|мин)?(?:\s|$)`)
)

func parseAmount(num, unit string) float64 {
	words := map[string]float64{"пару": 2, "пять": 5, "десять": 10, "пятнадцать": 15, "двадцать": 20, "тридцать": 30,
		"сорок": 40, "пятьдесят": 50, "полторы": 1.5, "одну": 1, "один": 1, "два": 2, "две": 2, "три": 3, "четыре": 4}
	var v float64
	switch num {
	case "полминуты":
		return 30
	case "минуту":
		return 60
	default:
		if n, err := strconv.ParseFloat(num, 64); err == nil {
			v = n
		} else {
			v = words[num]
		}
	}
	if strings.HasPrefix(unit, "мин") || (unit == "" && num == "пару") || num == "полторы" {
		return v * 60
	}
	return v
}

var youtubeRe = regexp.MustCompile(`(?i)(?:(?:с|на|из|в|через)\s+)?(?:ютуб\S*|youtube\S*|ютьюб\S*)(?:\s+(?:music|мьюзик))?`)

// youtubeRequest — «включи с ютуба Imagine Dragons» → «Imagine Dragons».
func youtubeRequest(text string) (string, bool) {
	if recommendationRequest(text) {
		return text, true
	}
	if _, ok := queueRequest(text); ok {
		return "", false
	}
	if _, _, _, ok := musicCommand(text); ok {
		return "", false
	}
	t := strings.ToLower(text)
	if !strings.Contains(t, "ютуб") && !strings.Contains(t, "youtube") && !strings.Contains(t, "ютьюб") {
		return "", false
	}
	verb := false
	for _, v := range []string{"включи", "поставь", "запусти", "сыграй", "найди", "играй", "вруби", "давай"} {
		if strings.Contains(t, v) {
			verb = true
		}
	}
	if !verb {
		return "", false
	}
	q := youtubeRe.ReplaceAllString(text, " ")
	for _, w := range []string{"джарвис", "включи", "поставь", "запусти", "сыграй", "найди", "играй", "вруби", "давай",
		"пожалуйста", "песню", "песня", "трек", "музыку", "мне"} {
		q = regexp.MustCompile(`(?i)(^|[\s,.!?])`+w+`([\s,.!?]|$)`).ReplaceAllString(q, " ")
	}
	q = strings.Trim(strings.Join(strings.Fields(q), " "), " ,.!?")
	if q == "" {
		return "", false
	}
	return q, true
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Warm — заранее разобрать плеер YouTube (на колонке это ~11 с) и потом раз в час проверять, не вышла ли
// новая версия: первая песня тогда включается без этой задержки.
func (m *Music) Warm(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if _, err := m.js.prepareWithUse(c, m.client(), false); err != nil {
			log.Printf("музыка: заранее подготовить плеер YouTube не вышло: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// Pos — где сейчас играет (или стоит на паузе), секунды.
func (m *Music) Pos() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pos
}

// PausedSince — когда поставили на паузу (нулевое время — не на паузе).
func (m *Music) PausedSince() time.Time {
	if m == nil {
		return time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.paused {
		return time.Time{}
	}
	return m.pausedAt
}
