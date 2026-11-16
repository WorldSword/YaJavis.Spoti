package main

// Spotify — управление Soloist (клиент Spotify Connect), который владелец сам поставил на колонку.
// YaJavis только находит его и вызывает его команды: движок Spotify проприетарный и в релиз не входит.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// spotifyExec запускает команду на колонке. В тестах подменяется.
type spotifyExec func(ctx context.Context, name string, args ...string) ([]byte, error)

type Spotify struct {
	root   string
	exec   spotifyExec
	OnPlay func() // зазвучало (включили или продолжили): остальная музыка Джарвиса должна замолчать
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
	return &Spotify{root: root, exec: exec}
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
	_, err := s.exec(ctx, "sh", append([]string{path.Join(s.root, "scripts", "control.sh")}, args...)...)
	return err
}

// Play — найти трек в Spotify и включить. Запрос уходит одним аргументом, без оболочки.
func (s *Spotify) Play(ctx context.Context, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("что включить?")
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
