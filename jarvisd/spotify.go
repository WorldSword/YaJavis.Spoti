package main

// Spotify — управление Soloist (клиент Spotify Connect), который владелец сам поставил на колонку.
// YaJavis только находит его и вызывает его команды: движок Spotify проприетарный и в релиз не входит.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/newfpv/YaJavis/installer/soloist"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// spotifyExec запускает команду на колонке. В тестах подменяется.
type spotifyExec func(ctx context.Context, name string, args ...string) ([]byte, error)

type Spotify struct {
	root   string
	exec   spotifyExec
	clock  func() time.Time
	poll   time.Duration // как часто Run спрашивает Soloist, что играет
	inst   *soloist.Installer
	tuner  *soloist.Tuner
	tuneMu sync.Mutex
	cmds   atomic.Uint64 // сколько команд ушло в Soloist; по нему видно, вмешался ли кто-то
	OnPlay func()        // зазвучало (включили или продолжили): остальная музыка Джарвиса должна замолчать

	// OnLearnLiked вызывается, когда Джарвис впервые увидел плейлист «Любимые треки» этого пользователя
	OnLearnLiked func(uri string)

	mu      sync.Mutex
	last    SpotifyNow // последний удачный ответ Soloist
	next    []Track    // следующие треки очереди Spotify
	volSeen int        // последняя громкость Spotify плюс 1; 0 — ещё не знаем
	skipped string     // голос DJ, который уже пропустили
	nextFor string     // для какого трека снята очередь
	liked   string     // адрес плейлиста «Любимые треки»
	lastAt  time.Time
	ok      bool
}

// soloistRoots — где Soloist может лежать: установка через YAM, затем автономная.
var soloistRoots = []string{"/data/yam/share/horohi.soloist", "/data/opt/soloist"}

func NewSpotify() *Spotify {
	roots := soloistRoots
	if d := os.Getenv("SOLOIST_DIR"); d != "" {
		roots = append([]string{d}, roots...)
	}
	return newSpotifyAt(findSoloistRoot(roots...), osSpotifyExec)
}

func newSpotifyAt(root string, exec spotifyExec) *Spotify {
	return &Spotify{root: root, exec: exec, clock: time.Now, poll: 2 * time.Second, inst: soloist.New(soloist.Exec(exec), "jarvisd/"+version)}
}

// findSoloistRoot — первая папка, где лежит scripts/control.sh Soloist; пусто, если Soloist не найден.
func findSoloistRoot(candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(path.Join(c, "scripts", "control.sh")); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func osSpotifyExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Restart перезапускает службу Soloist: через YAM или автономным soloist-cli. Служба остаётся жить после команды
// и держала бы канал вывода, поэтому вывод команды отбрасывается.
func (s *Spotify) Restart(ctx context.Context) error {
	if !s.Installed() {
		return errors.New("Soloist не найден")
	}
	if s.exec == nil {
		return errors.New("нечем запускать команды")
	}
	args := []string{"sh", path.Join(s.root, "bin", "soloist-cli"), "restart"}
	if yam, ok := yamRootOf(s.root); ok {
		args = []string{path.Join(yam, "bin", "soloist"), "restart"}
	}
	_, err := s.exec(ctx, "sh", append([]string{"-c", `"$@" >/dev/null 2>&1 </dev/null`, "sh"}, args...)...)
	return err
}

// Installer — установка Soloist из панели; у nil-Spotify его нет.
func (s *Spotify) Installer() *soloist.Installer {
	if s == nil {
		return nil
	}
	return s.inst
}

// Tuner — подбор звукового устройства для этой колонки; у nil-Spotify и без Soloist его нет.
func (s *Spotify) Tuner() *soloist.Tuner {
	if !s.Installed() {
		return nil
	}
	s.tuneMu.Lock()
	defer s.tuneMu.Unlock()
	if s.tuner == nil {
		s.tuner = soloist.NewTuner(s.root, soloist.Exec(s.exec), s.Restart)
	}
	return s.tuner
}

// CmdSeq — число отправленных в Soloist команд.
func (s *Spotify) CmdSeq() uint64 {
	if s == nil {
		return 0
	}
	return s.cmds.Load()
}

func (s *Spotify) Installed() bool { return s != nil && s.root != "" }
func (s *Spotify) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Spotify) control(ctx context.Context, args ...string) error {
	if !s.Installed() {
		return errors.New("Soloist не найден на колонке")
	}
	s.cmds.Add(1)
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	out, err := s.exec(ctx, "sh", append([]string{path.Join(s.root, "scripts", "control.sh")}, args...)...)
	if err != nil {
		if last := lastLine(out); last != "" {
			log.Printf("spotify: %v; вывод Soloist: %s", err, strings.TrimSpace(string(out)))
			return fmt.Errorf("%w (%s)", err, last)
		}
	}
	return err
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(l); len(r) > 160 {
		l = string(r[:160]) + "…"
	}
	return l
}

// searchKeysSet — есть ли у Soloist Client ID и Secret: без них его голосовой поиск не работает.
func (s *Spotify) searchKeysSet() bool {
	_, api := soloistSettings(s)
	return api["SPOTIFY_CLIENT_ID"] != "" && api["SPOTIFY_CLIENT_SECRET"] != ""
}

// Play — найти трек в Spotify и включить. Запрос уходит одним аргументом, без оболочки.
func (s *Spotify) Play(ctx context.Context, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("что включить?")
	}
	if s.Installed() && !s.searchKeysSet() {
		return errors.New("для поиска в Spotify голосом нужны Client ID и Secret: добавьте их в панели, Подключения → Spotify")
	}
	if err := s.control(ctx, "search", query); err != nil {
		return err
	}
	if s.OnPlay != nil {
		s.OnPlay()
	}
	return nil
}

var spotifyActions = map[string]string{"pause": "pause", "resume": "play", "next": "next", "prev": "prev", "toggle": "toggle"}

// Control — pause | resume | next | prev | toggle.
func (s *Spotify) Control(ctx context.Context, action string) error {
	arg, ok := spotifyActions[action]
	if !ok {
		return errors.New("неизвестное действие Spotify: " + action)
	}
	if err := s.control(ctx, arg); err != nil {
		return err
	}
	if action == "resume" && s.OnPlay != nil {
		s.OnPlay()
	}
	return nil
}

var spotifyRe = regexp.MustCompile(`(?i)(^|[\s,.!?])(?:(?:в|на|через|из|с)\s+)?(?:spotify\S*|спотиф\S*|спотик\S*)`)

var spotifyFillers = func() []*regexp.Regexp {
	var res []*regexp.Regexp
	for _, w := range []string{"джарвис", "включи", "поставь", "запусти", "сыграй", "найди", "играй", "вруби", "давай",
		"пожалуйста", "песню", "песня", "трек", "музыку", "мне"} {
		res = append(res, regexp.MustCompile(`(?i)(^|[\s,.!?])`+w+`([\s,.!?]|$)`))
	}
	return res
}()

// spotifyRequest — «включи в Spotify Linkin Park» → «Linkin Park». Без слова Spotify и без глагола — не наш запрос.
// «Включи в Spotify» без названия → пустой запрос и ok.
func spotifyRequest(text string) (string, bool) {
	if _, _, _, ok := musicCommand(text); ok {
		return "", false
	}
	if !spotifyRe.MatchString(text) {
		return "", false
	}
	t := strings.ToLower(text)
	verb := false
	for _, v := range []string{"включи", "поставь", "запусти", "сыграй", "найди", "играй", "вруби", "давай"} {
		if strings.Contains(t, v) {
			verb = true
		}
	}
	if !verb {
		return "", false
	}
	q := spotifyRe.ReplaceAllString(text, " ")
	for _, re := range spotifyFillers {
		q = re.ReplaceAllString(q, " ")
	}
	return strings.Trim(strings.Join(strings.Fields(q), " "), " ,.!?"), true
}

// SpotifyNow — что сейчас у Soloist.
type SpotifyNow struct {
	Playing                 bool
	Title, Artist           string
	Cover, URI              string
	ContextURI, ContextName string  // плейлист, альбом или радио, из которого играет трек
	Pos, Dur                float64 // секунды
	Volume                  int     // громкость Spotify 0–100; -1, если Soloist её не назвал
}

type soloistIdentity struct {
	Decorations struct {
		Identity struct {
			Name string `json:"name"`
		} `json:"identity"`
	} `json:"decorations"`
}

// parseSpotifyNow разбирает `soloist ctl now --json`. Форма снята с живой колонки (Soloist 1.3.13):
// status, item.uri, название в item.decorations.identity.name, исполнители в decorations.creators[].entity,
// обложки в decorations.visual_identity.cover[], длительность в decorations.playback.duration_ms, позиция в position.position_ms.
func parseSpotifyNow(b []byte) (SpotifyNow, error) {
	// Вывод команды может идти вместе с журналом Soloist: берём только сам объект.
	if i, j := bytes.IndexByte(b, '{'), bytes.LastIndexByte(b, '}'); i >= 0 && j > i {
		b = b[i : j+1]
	}
	var raw struct {
		Status string `json:"status"`
		Item   *struct {
			URI         string `json:"uri"`
			Decorations struct {
				Identity struct {
					Name string `json:"name"`
				} `json:"identity"`
				Visual struct {
					Cover []struct {
						URL  string `json:"url"`
						Size string `json:"size"`
					} `json:"cover"`
				} `json:"visual_identity"`
				Creators []struct {
					Entity soloistIdentity `json:"entity"`
				} `json:"creators"`
				Playback struct {
					DurationMs float64 `json:"duration_ms"`
				} `json:"playback"`
			} `json:"decorations"`
		} `json:"item"`
		Context struct {
			URI         string `json:"uri"`
			Decorations struct {
				Identity struct {
					Name string `json:"name"`
				} `json:"identity"`
			} `json:"decorations"`
		} `json:"context"`
		Position struct {
			Ms float64 `json:"position_ms"`
		} `json:"position"`
		Volume *int `json:"volume"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return SpotifyNow{}, err
	}
	if raw.Item == nil {
		return SpotifyNow{}, nil
	}
	it, d := raw.Item, raw.Item.Decorations
	now := SpotifyNow{Playing: raw.Status == "playing", Title: d.Identity.Name, URI: it.URI, Pos: raw.Position.Ms / 1000, Dur: d.Playback.DurationMs / 1000,
		ContextURI: raw.Context.URI, ContextName: raw.Context.Decorations.Identity.Name, Volume: -1}
	if raw.Volume != nil {
		now.Volume = *raw.Volume
	}
	if now.Title == "" && now.URI != "" {
		now.Title = "Spotify"
	}
	names := make([]string, 0, len(d.Creators))
	for _, c := range d.Creators {
		if n := c.Entity.Decorations.Identity.Name; n != "" {
			names = append(names, n)
		}
	}
	now.Artist = strings.Join(names, ", ")
	for _, c := range d.Visual.Cover { // «default» — 300 пикселей, хватает для карточки
		if c.Size == "default" {
			now.Cover = c.URL
		}
	}
	if now.Cover == "" && len(d.Visual.Cover) > 0 {
		now.Cover = d.Visual.Cover[0].URL
	}
	return now, nil
}

// ctl — `soloist ctl -w 127.0.0.1:9090 <args>` с библиотеками Soloist; если системный загрузчик не подходит,
// запасной запуск через загрузчик из lib64 (как в scripts/status.sh Soloist).
func (s *Spotify) ctl(ctx context.Context, args ...string) ([]byte, error) {
	bin, lib := path.Join(s.root, "bin", "soloist"), path.Join(s.root, "lib64")
	ctl := append([]string{"ctl", "-w", "127.0.0.1:9090"}, args...)
	env := "LD_LIBRARY_PATH=" + lib
	out, err := s.exec(ctx, "env", append([]string{env, bin}, ctl...)...)
	if ld := path.Join(lib, "ld-linux-aarch64.so.1"); err != nil {
		if _, serr := os.Stat(ld); serr == nil {
			out, err = s.exec(ctx, "env", append([]string{env, ld, "--library-path", lib, "--argv0", bin, bin}, ctl...)...)
		}
	}
	return out, err
}

// Seek — перемотка на позицию в секундах.
func (s *Spotify) Seek(ctx context.Context, sec float64) error {
	if !s.Installed() {
		return errors.New("Soloist не найден на колонке")
	}
	if sec < 0 || sec > 24*3600 {
		return errors.New("недопустимая позиция")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := s.ctl(ctx, "seek", strconv.FormatInt(int64(sec*1000), 10))
	if err != nil {
		if last := lastLine(out); last != "" {
			return fmt.Errorf("%w (%s)", err, last)
		}
	}
	return err
}

// Now — спросить Soloist, что играет, и запомнить ответ.
func (s *Spotify) Now(ctx context.Context) (SpotifyNow, error) {
	if !s.Installed() {
		return SpotifyNow{}, errors.New("Soloist не найден на колонке")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := s.ctl(ctx, "now", "--json")
	var now SpotifyNow
	if err == nil {
		now, err = parseSpotifyNow(out)
	}
	s.mu.Lock()
	if err != nil {
		s.last, s.ok = SpotifyNow{}, false
		s.mu.Unlock()
		return SpotifyNow{}, err
	}
	s.last, s.lastAt, s.ok = now, s.clock(), true
	learn := s.learnLiked(now)
	vol, changed := s.volumeChanged(now.Volume)
	s.mu.Unlock()
	if changed {
		s.applyVolume(ctx, vol)
	}
	if isDJVoice(now.URI) {
		s.skipDJVoice(ctx, now.URI)
	}
	if learn != "" && s.OnLearnLiked != nil {
		s.OnLearnLiked(learn)
	}
	return now, nil
}

func (s *Spotify) active() bool {
	return s.ok && s.last.URI != "" && s.clock().Sub(s.lastAt) <= 3*time.Second
}

// Running — Soloist недавно отвечал (даже если сейчас ничего не играет).
func (s *Spotify) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ok && s.clock().Sub(s.lastAt) <= 3*time.Second
}

func (s *Spotify) Active() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active()
}

func (s *Spotify) Playing() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active() && s.last.Playing
}

func (s *Spotify) Paused() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active() && !s.last.Playing
}

// Snapshot — для страницы «Музыка».
func (s *Spotify) Snapshot() MusicState {
	if s == nil {
		return MusicState{Source: "spotify"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active() {
		return MusicState{Source: "spotify"}
	}
	n := s.last
	return MusicState{Source: "spotify", Cover: n.Cover, Playing: n.Playing, Paused: !n.Playing,
		Track: Track{ID: n.URI, Title: n.Title, Author: n.Artist}, Pos: n.Pos, Dur: n.Dur, Next: append([]Track(nil), s.next...)}
}

// Run — раз в s.poll спрашивать Soloist, что играет, пока не отменят ctx.
func (s *Spotify) Run(ctx context.Context) {
	if !s.Installed() {
		return
	}
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		if now, err := s.Now(ctx); err == nil && now.URI != "" {
			s.refreshQueueIfChanged(ctx, now.URI)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// likedNames — как Spotify называет плейлист «Любимые треки» в разных языках.
var likedNames = map[string]bool{"liked songs": true, "любимые треки": true, "понравившиеся": true, "избранное": true, "любимые песни": true}

// learnLiked запоминает адрес «Любимых»; вызывать под s.mu. Возвращает адрес, если он новый.
func (s *Spotify) learnLiked(n SpotifyNow) string {
	if n.ContextURI == "" || !likedNames[strings.ToLower(strings.TrimSpace(n.ContextName))] || n.ContextURI == s.liked {
		return ""
	}
	s.liked = n.ContextURI
	return n.ContextURI
}

// Liked — адрес плейлиста «Любимые треки»; пусто, пока Джарвис его не видел.
func (s *Spotify) Liked() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liked
}

// SetLiked — подставить сохранённый ранее адрес «Любимых».
func (s *Spotify) SetLiked(uri string) {
	if s == nil || !validSpotifyURI(uri) {
		return
	}
	s.mu.Lock()
	s.liked = uri
	s.mu.Unlock()
}

var spotifyURIRe = regexp.MustCompile(`^spotify:[a-z]+(?::[A-Za-z0-9_]+)+$`)

func validSpotifyURI(u string) bool { return spotifyURIRe.MatchString(u) }

// PlayURI включает адрес Spotify (трек, плейлист, радио) в Soloist.
func (s *Spotify) PlayURI(ctx context.Context, uri string) error {
	if !s.Installed() {
		return errors.New("Soloist не найден на колонке")
	}
	if !validSpotifyURI(uri) {
		return errors.New("недопустимый адрес Spotify")
	}
	s.cmds.Add(1)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if out, err := s.ctl(ctx, "play", uri); err != nil {
		log.Printf("spotify: play %s: %v; вывод: %s", uri, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("Soloist не включил: %w", err)
	}
	if s.OnPlay != nil {
		s.OnPlay()
	}
	return nil
}

// djURI — плейлист «DJ» Spotify: один на всех.
const djURI = "spotify:playlist:37i9dQZF1EYkqdzj48dyYq"

func (s *Spotify) PlayDJ(ctx context.Context) error { return s.PlayURI(ctx, djURI) }

// PlayLiked включает «Любимые треки». Адрес узнаём, когда они хоть раз играли в Spotify.
func (s *Spotify) PlayLiked(ctx context.Context) error {
	u := s.Liked()
	if u == "" {
		return errors.New("не знаю, где ваши любимые треки: включите их один раз в приложении Spotify на эту колонку, дальше Джарвис запомнит")
	}
	return s.PlayURI(ctx, u)
}

// PlayRadio включает радио по текущему треку: после него Spotify сам подбирает похожие.
func (s *Spotify) PlayRadio(ctx context.Context) error {
	s.mu.Lock()
	u := s.last.URI
	s.mu.Unlock()
	if id, ok := strings.CutPrefix(u, "spotify:track:"); ok && id != "" {
		return s.PlayURI(ctx, "spotify:station:track:"+id)
	}
	return errors.New("сейчас не играет трек, по которому можно включить радио")
}

// parseSpotifyQueue разбирает `soloist ctl queue --json`: поле upcoming — следующие треки.
func parseSpotifyQueue(b []byte) ([]Track, error) {
	if i, j := bytes.IndexByte(b, '{'), bytes.LastIndexByte(b, '}'); i >= 0 && j > i {
		b = b[i : j+1]
	}
	var raw struct {
		Upcoming []struct {
			Item struct {
				URI         string `json:"uri"`
				Decorations struct {
					Identity struct {
						Name string `json:"name"`
					} `json:"identity"`
					Visual struct {
						Cover []struct {
							URL  string `json:"url"`
							Size string `json:"size"`
						} `json:"cover"`
					} `json:"visual_identity"`
					Creators []struct {
						Entity soloistIdentity `json:"entity"`
					} `json:"creators"`
				} `json:"decorations"`
			} `json:"item"`
		} `json:"upcoming"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(raw.Upcoming))
	for _, u := range raw.Upcoming {
		if isDJVoice(u.Item.URI) {
			continue
		}
		d := u.Item.Decorations
		t := Track{ID: u.Item.URI, Title: d.Identity.Name}
		var names []string
		for _, c := range d.Creators {
			if n := c.Entity.Decorations.Identity.Name; n != "" {
				names = append(names, n)
			}
		}
		t.Author = strings.Join(names, ", ")
		for _, c := range d.Visual.Cover {
			if c.Size == "small" || t.Art == "" {
				t.Art = c.URL
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// RefreshQueue снимает очередь Soloist и кладёт её в снимок для панели.
func (s *Spotify) RefreshQueue(ctx context.Context) {
	if !s.Installed() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := s.ctl(ctx, "queue", "--json")
	var q []Track
	if err == nil {
		q, err = parseSpotifyQueue(out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.next = nil
		return
	}
	s.next, s.nextFor = q, s.last.URI
}

func (s *Spotify) refreshQueueIfChanged(ctx context.Context, uri string) {
	s.mu.Lock()
	same := s.nextFor == uri
	s.mu.Unlock()
	if !same {
		s.RefreshQueue(ctx)
	}
}

// volumeChanged — громкость в Spotify сменили с прошлого опроса. Первое значение только запоминаем:
// при запуске громкость колонки не трогаем. Вызывать под s.mu.
func (s *Spotify) volumeChanged(v int) (int, bool) {
	if v < 0 || v > 100 {
		return v, false
	}
	prev := s.volSeen
	s.volSeen = v + 1 // +1: ноль значения означает «ещё не знаем»
	return v, prev != 0 && prev != v+1
}

// applyVolume переносит громкость из приложения Spotify на колонку: Soloist запущен с APULSE_EXTERNAL_VOLUME=1
// и сам звук не регулирует, поэтому ALSA Master ставим мы.
func (s *Spotify) applyVolume(ctx context.Context, v int) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if out, err := s.exec(ctx, "amixer", "sset", "Master", strconv.Itoa(v)+"%"); err != nil {
		log.Printf("spotify: громкость %d%% не применилась: %v %s", v, err, strings.TrimSpace(string(out)))
	}
}

// isDJVoice — «вставка» DJ X (Up next, Далее): spotify:media:…, это озвучка, а не трек.
func isDJVoice(uri string) bool { return strings.HasPrefix(uri, "spotify:media:") }

// skipDJVoice пропускает голос DJ X: Soloist его проиграть не умеет, и приложение на телефоне зависает,
// пока ждёт конца вставки. Одну и ту же вставку пропускаем один раз; пользовательской командой это не считается.
func (s *Spotify) skipDJVoice(ctx context.Context, uri string) {
	s.mu.Lock()
	done := s.skipped == uri
	s.skipped = uri
	s.mu.Unlock()
	if done {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, err := s.ctl(ctx, "next"); err != nil {
		log.Printf("spotify: не удалось пропустить голос DJ: %v %s", err, strings.TrimSpace(string(out)))
		s.mu.Lock()
		s.skipped = "" // попробуем при следующем опросе
		s.mu.Unlock()
		return
	}
	log.Printf("spotify: пропустил голосовую вставку DJ")
}
