package main

// Spotify — управление Soloist (клиент Spotify Connect), который владелец сам поставил на колонку.
// YaJavis только находит его и вызывает его команды: движок Spotify проприетарный и в релиз не входит.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// spotifyExec запускает команду на колонке. В тестах подменяется.
type spotifyExec func(ctx context.Context, name string, args ...string) ([]byte, error)

type Spotify struct {
	root   string
	exec   spotifyExec
	clock  func() time.Time
	poll   time.Duration // как часто Run спрашивает Soloist, что играет
	OnPlay func()        // зазвучало (включили или продолжили): остальная музыка Джарвиса должна замолчать

	mu     sync.Mutex
	last   SpotifyNow // последний удачный ответ Soloist
	lastAt time.Time
	ok     bool
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
	return &Spotify{root: root, exec: exec, clock: time.Now, poll: 2 * time.Second}
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

// SpotifyNow — что сейчас у Soloist. Имена полей JSON — по README Soloist; сверить с реальным `now --json` на колонке.
type SpotifyNow struct {
	Playing       bool
	Title, Artist string
	Cover, URI    string
	Pos, Dur      float64 // секунды
}

func parseSpotifyNow(b []byte) (SpotifyNow, error) {
	// Вывод команды может идти вместе с журналом Soloist: берём только сам объект.
	if i, j := bytes.IndexByte(b, '{'), bytes.LastIndexByte(b, '}'); i >= 0 && j > i {
		b = b[i : j+1]
	}
	var raw struct {
		IsPlaying  bool    `json:"is_playing"`
		ProgressMs float64 `json:"progress_ms"`
		Item       *struct {
			URI        string  `json:"uri"`
			Name       string  `json:"name"`
			DurationMs float64 `json:"duration_ms"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"album"`
		} `json:"item"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return SpotifyNow{}, err
	}
	if raw.Item == nil {
		return SpotifyNow{}, nil
	}
	it := raw.Item
	now := SpotifyNow{Playing: raw.IsPlaying, Title: it.Name, URI: it.URI, Pos: raw.ProgressMs / 1000, Dur: it.DurationMs / 1000}
	if now.Title == "" && now.URI != "" {
		now.Title = "Spotify"
	}
	names := make([]string, 0, len(it.Artists))
	for _, a := range it.Artists {
		names = append(names, a.Name)
	}
	now.Artist = strings.Join(names, ", ")
	if len(it.Album.Images) > 0 {
		now.Cover = it.Album.Images[0].URL
	}
	return now, nil
}

// Now — спросить Soloist, что играет, и запомнить ответ.
func (s *Spotify) Now(ctx context.Context) (SpotifyNow, error) {
	if !s.Installed() {
		return SpotifyNow{}, errors.New("Soloist не найден на колонке")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	bin, lib := path.Join(s.root, "bin", "soloist"), path.Join(s.root, "lib64")
	ctl := []string{"ctl", "-w", "127.0.0.1:9090", "now", "--json"}
	env := "LD_LIBRARY_PATH=" + lib
	out, err := s.exec(ctx, "env", append([]string{env, bin}, ctl...)...)
	if ld := path.Join(lib, "ld-linux-aarch64.so.1"); err != nil {
		if _, serr := os.Stat(ld); serr == nil { // как в scripts/status.sh Soloist: свой загрузчик, если системный не подходит
			out, err = s.exec(ctx, "env", append([]string{env, ld, "--library-path", lib, "--argv0", bin, bin}, ctl...)...)
		}
	}
	var now SpotifyNow
	if err == nil {
		now, err = parseSpotifyNow(out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.last, s.ok = SpotifyNow{}, false
		return SpotifyNow{}, err
	}
	s.last, s.lastAt, s.ok = now, s.clock(), true
	return now, nil
}

// active — есть свежий ответ Soloist с треком; s.mu занят.
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
		Track: Track{ID: n.URI, Title: n.Title, Author: n.Artist}, Pos: n.Pos, Dur: n.Dur}
}

// Run — раз в s.poll спрашивать Soloist, что играет, пока не отменят ctx.
func (s *Spotify) Run(ctx context.Context) {
	if !s.Installed() {
		return
	}
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		_, _ = s.Now(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
