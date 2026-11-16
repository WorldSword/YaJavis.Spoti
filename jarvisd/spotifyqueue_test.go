package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Очередь с живой колонки: soloist ctl queue --json, Soloist 1.3.13.
func TestParseSpotifyQueue(t *testing.T) {
	q, err := parseSpotifyQueue(fixture(t, "soloist_queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 10 {
		t.Fatalf("треков в очереди %d, ждали 10", len(q))
	}
	first := q[0]
	if first.Title != "так похуй" || !strings.HasPrefix(first.ID, "spotify:track:") || first.Author == "" || !strings.HasPrefix(first.Art, "https://i.scdn.co/") {
		t.Errorf("первый трек разобран неверно: %+v", first)
	}
}

func TestParseSpotifyQueueNoise(t *testing.T) {
	if q, err := parseSpotifyQueue([]byte("log line\n{\"type\":\"queue_changed\",\"previous\":[],\"upcoming\":[]}\n")); err != nil || len(q) != 0 {
		t.Errorf("пустая очередь среди журнала: %v %v", q, err)
	}
	if _, err := parseSpotifyQueue([]byte("не json")); err == nil {
		t.Error("мусор должен давать ошибку")
	}
}

// Плейлист-контекст: из него берём «любимые» и DJ.
func TestParseSpotifyNowContext(t *testing.T) {
	n, err := parseSpotifyNow(fixture(t, "soloist_now_dj.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n.ContextURI != "spotify:playlist:37i9dQZF1EYkqdzj48dyYq" || n.ContextName != "DJ" {
		t.Errorf("контекст: %q %q", n.ContextURI, n.ContextName)
	}
}

type recExec struct {
	mu    sync.Mutex
	calls [][]string
	queue []byte
	now   []byte
}

func (r *recExec) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	n := len(args)
	switch {
	case n >= 2 && args[n-2] == "queue" && args[n-1] == "--json":
		return r.queue, nil
	case n >= 2 && args[n-2] == "now" && args[n-1] == "--json":
		return r.now, nil
	}
	return nil, nil
}

func (r *recExec) played() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if n := len(c); n >= 2 && c[n-2] == "play" {
			out = append(out, c[n-1])
		}
	}
	return out
}

func newQueueSpotify(t *testing.T, r *recExec) *Spotify {
	t.Helper()
	return newSpotifyAt(fakeSoloistRoot(t), r.run)
}

func TestPlayURIValidates(t *testing.T) {
	r := &recExec{}
	sp := newQueueSpotify(t, r)
	for _, bad := range []string{"", "http://x", "spotify:track:abc;reboot", "spotify:track:a b", "track:123", "spotify:"} {
		if err := sp.PlayURI(context.Background(), bad); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
	if len(r.played()) != 0 {
		t.Errorf("плохие адреса дошли до Soloist: %v", r.played())
	}
	if err := sp.PlayURI(context.Background(), "spotify:playlist:37i9dQZF1EYkqdzj48dyYq"); err != nil {
		t.Fatal(err)
	}
	if got := r.played(); len(got) != 1 || got[0] != "spotify:playlist:37i9dQZF1EYkqdzj48dyYq" {
		t.Errorf("play: %v", got)
	}
}

func TestPlayDJAndRadio(t *testing.T) {
	r := &recExec{now: []byte(spotifyNowFixture)}
	sp := newQueueSpotify(t, r)
	if err := sp.PlayDJ(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.played(); got[len(got)-1] != "spotify:playlist:37i9dQZF1EYkqdzj48dyYq" {
		t.Errorf("DJ: %v", got)
	}
	if err := sp.PlayRadio(context.Background()); err == nil {
		t.Error("радио без известного трека должно вернуть ошибку")
	}
	if _, err := sp.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sp.PlayRadio(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.played()
	if !strings.HasPrefix(got[len(got)-1], "spotify:station:track:") {
		t.Errorf("радио по треку: %v", got)
	}
}

// «Любимые» запоминаются, когда пользователь хоть раз включил их в Spotify.
func TestLikedLearnedAndPlayed(t *testing.T) {
	liked := strings.Replace(string(fixture(t, "soloist_now_dj.json")),
		`"uri":"spotify:playlist:37i9dQZF1EYkqdzj48dyYq","entity_type":"playlist","decorations":{"identity":{"name":"DJ"}`,
		`"uri":"spotify:playlist:37i9dQZF1F5p3rmiWPIYgZ","entity_type":"playlist","decorations":{"identity":{"name":"Liked Songs"}`, 1)
	r := &recExec{now: []byte(liked)}
	sp := newQueueSpotify(t, r)
	var learned string
	sp.OnLearnLiked = func(u string) { learned = u }
	if err := sp.PlayLiked(context.Background()); err == nil {
		t.Error("пока «любимые» неизвестны, ждали ошибку с подсказкой")
	}
	if _, err := sp.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	if learned != "spotify:playlist:37i9dQZF1F5p3rmiWPIYgZ" || sp.Liked() != learned {
		t.Errorf("не запомнил любимые: %q", learned)
	}
	if err := sp.PlayLiked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.played(); got[len(got)-1] != learned {
		t.Errorf("play: %v", got)
	}
}

// После смены трека панель получает следующие треки списком.
func TestSnapshotHasUpcoming(t *testing.T) {
	r := &recExec{now: fixture(t, "soloist_now_dj.json"), queue: fixture(t, "soloist_queue.json")}
	sp := newQueueSpotify(t, r)
	if _, err := sp.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	sp.RefreshQueue(context.Background())
	snap := sp.Snapshot()
	if len(snap.Next) != 10 || snap.Next[0].Title != "так похуй" || snap.Next[0].Art == "" {
		t.Errorf("Next в снимке: %+v", snap.Next)
	}
	if snap.Queue != 0 {
		t.Errorf("Queue (ручная очередь YouTube) должен быть 0, а не %d", snap.Queue)
	}
}

func TestSpotifyContextRequest(t *testing.T) {
	cases := []struct {
		in   string
		kind string
		ok   bool
	}{
		{"включи в спотифае мои любимые треки", "liked", true},
		{"джарвис включи мои лайки в спотифай", "liked", true},
		{"включи в Spotify избранное", "liked", true},
		{"включи в spotify liked songs", "liked", true},
		{"включи в спотифай диджея", "dj", true},
		{"включи DJ в спотифае", "dj", true},
		{"включи радио по этому треку в спотифай", "radio", true},
		{"запусти в спотифае радио по текущему треку", "radio", true},
		{"включи в спотифае Linkin Park", "", false},
		{"включи любимые треки", "", false}, // без слова Spotify это не наш запрос (решает вызывающий)
	}
	for _, c := range cases {
		kind, ok := spotifyContextRequest(c.in)
		if ok != c.ok || kind != c.kind {
			t.Errorf("%q → %q,%v; ждали %q,%v", c.in, kind, ok, c.kind, c.ok)
		}
	}
	// когда Spotify уже играет, слово «Spotify» не обязательно
	if kind, ok := spotifyContextRequestOwned("включи радио по этому треку"); !ok || kind != "radio" {
		t.Errorf("при играющем Spotify: %q %v", kind, ok)
	}
	if _, ok := spotifyContextRequestOwned("включи мои любимые треки"); !ok {
		t.Error("любимые при играющем Spotify")
	}
}

func TestPostMusicSpotifyKinds(t *testing.T) {
	r := &recExec{now: []byte(spotifyNowFixture)}
	w := spotifyWeb(t, fakeSoloistRoot(t), r.run)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		w.postMusic(rec, httptest.NewRequest(http.MethodPost, "/api/music", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"action":"spotify","query":"dj"}`); rec.Code != 200 {
		t.Fatalf("dj: код %d: %s", rec.Code, rec.Body)
	}
	if got := r.played(); len(got) != 1 || got[0] != djURI {
		t.Errorf("DJ не включился: %v", got)
	}
	if rec := post(`{"action":"spotify","query":"nonsense"}`); rec.Code != 400 {
		t.Errorf("неизвестный вид: код %d", rec.Code)
	}
	// после включения DJ Джарвис увидел в Now «Любимые» из fixture и запомнил их
	if rec := post(`{"action":"spotify","query":"liked"}`); rec.Code != 200 {
		t.Errorf("любимые: код %d: %s", rec.Code, rec.Body)
	}
	if got := r.played(); got[len(got)-1] != "spotify:playlist:p" {
		t.Errorf("любимые: %v", got)
	}
}
