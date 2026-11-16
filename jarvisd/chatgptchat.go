package main

// Общая память Джарвиса: один разговор на все звонки и всех ассистентов. Устроена как у Codex и Claude, но на годы.
//   - Архив: каждая реплика с датой и тем, кто сказал, дописывается в chatgpt-chat-archive.jsonl (последние ~5000).
//   - Досье «о хозяине»: имя, близкие, вкусы, привычки — копится всё время и переживает «Начать новый чат».
//   - Конспект: договорённости и хронология недавних разговоров. Когда реплик вне конспекта набирается много,
//     в паузе разговора текстовая модель дописывает старые реплики в досье и конспект (compact).
//   - Летопись: перед тем как старые реплики уходят из архива, модель сжимает их в запись месяца — ничего не пропадает.
//   - В начале звонка модель получает досье, конспект и последние реплики дословно, со временем.
//   - recall_conversation ищет по архиву и летописи, edit_memory забывает и исправляет («меня зовут не Сергей, а Павел»).
// «Начать новый чат» забывает конспект и недавние реплики (досье, летопись и архив остаются), «Стереть память» — всё.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	chatKeepChars    = 10000            // в начале звонка: досье, конспект и последние реплики — не больше стольких букв (длинное замедляет ответ)
	chatCompactAt    = 9000             // столько букв реплик вне конспекта — пора сжимать
	chatCompactKeep  = 4500             // последние реплики на столько букв остаются дословно
	chatCompactMax   = 24000            // за один проход сжимаем не больше стольких букв (остальное — следующим)
	chatProfileMax   = 2000             // досье (модель просим столько; обрезаем на полуторном)
	chatSummaryMax   = 2500             // конспект: договорённости и хронология
	chatChronicleMax = 1500             // запись летописи за месяц
	chatCompactIdle  = 45 * time.Second // сжимаем в паузе разговора, а не посреди него
	chatRecallMax    = 4000             // ответ «вспомни» не длиннее (без летописи)
	chatArchiveHard  = 8000             // сжимать в летопись нечем — выше этого старое просто выкидывается
	chatForgetQuiet  = 10 * time.Minute // после «забудь …» реплики про это не записываются (и сама просьба тоже)
)

var chatArchiveMax = 5000 // реплик в архиве дословно (~1–2 МБ); что старше — в летописи

type ChatTurn struct {
	N    int64  `json:"n,omitempty"` // порядковый номер реплики (растёт всегда, и после «Начать новый чат»)
	TS   int64  `json:"ts"`
	User string `json:"user,omitempty"`
	Bot  string `json:"bot,omitempty"`
}

func (t ChatTurn) size() int { return utf8.RuneCountInString(t.User) + utf8.RuneCountInString(t.Bot) }

// ChronMonth — запись летописи: месяц разговоров, сжатый моделью.
type ChronMonth struct {
	Month string `json:"month"` // 2026-11
	Text  string `json:"text"`
	Turns int    `json:"turns"` // сколько реплик в неё ушло
}

// Summarizer — текстовая модель без инструментов: instructions + text → ответ.
type Summarizer func(ctx context.Context, instructions, text string) (string, error)

type memEdit struct{ Forget, Remember string }

type forgetMark struct {
	terms []string
	until time.Time
}

type ChatThread struct {
	mu          sync.Mutex
	path        string
	ID          string       `json:"id"`
	Since       int64        `json:"since"`
	From        int64        `json:"from,omitempty"`         // первая реплика текущего чата; более ранние — только в архиве
	Next        int64        `json:"next,omitempty"`         // номер следующей реплики
	Profile     string       `json:"profile,omitempty"`      // досье о хозяине: переживает «Начать новый чат»
	Summary     string       `json:"summary,omitempty"`      // конспект реплик текущего чата с номерами ≤ SummaryUpto
	SummaryUpto int64        `json:"summary_upto,omitempty"` //
	SummaryAt   int64        `json:"summary_at,omitempty"`   // когда память обновлялась
	Chronicle   []ChronMonth `json:"chronicle,omitempty"`    // летопись по месяцам — старше архива
	Turns       []ChatTurn   `json:"turns,omitempty"`        // формат до 0.9.67: при загрузке переезжают в архив

	archive    []ChatTurn
	enabled    func() bool // галочка «Запоминать разговоры»; nil — всегда
	now        func() time.Time
	summarize  Summarizer
	compacting bool
	running    bool // maintain уже идёт
	again      bool // пока шёл — попросили ещё раз
	folding    bool // архив переполнен — сжимаем в летопись, пока не вернётся к chatArchiveMax
	timer      *time.Timer
	failUntil  time.Time
	profileRev int // досье поменяли («запомни») — сжатие, начатое раньше, досье не перезаписывает
	gen        int // «Стереть память» — всё, что считалось до неё, выбрасывается
	edits      []memEdit
	forgets    []forgetMark
}

// chatgptChat — общий чат (main); nil — без общей памяти.
var chatgptChat *ChatThread

func LoadChatThread(path string) *ChatThread {
	c := &ChatThread{path: path, now: time.Now}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, c)
	}
	c.loadArchive()
	migrated := false
	if len(c.Turns) > 0 && len(c.archive) == 0 { // старый формат: 40 реплик прямо в файле чата
		c.archive, migrated = c.Turns, true
	}
	c.Turns = nil
	if c.Next < 1 {
		c.Next = 1
	}
	for i := range c.archive {
		if c.archive[i].N == 0 {
			c.archive[i].N = c.Next
		}
		if c.archive[i].N >= c.Next {
			c.Next = c.archive[i].N + 1
		}
	}
	if c.From < 1 {
		c.From = 1
	}
	if c.ID == "" {
		c.ID, c.Since = newID(), time.Now().Unix()
	}
	if migrated || len(c.archive) > chatArchiveHard {
		if len(c.archive) > chatArchiveHard {
			c.archive = c.archive[len(c.archive)-chatArchiveMax:]
		}
		c.rewriteArchive()
	}
	c.saveLocked()
	return c
}

// SetClock — время хозяина (часовой пояс из настроек) для дат в памяти.
func (c *ChatThread) SetClock(now func() time.Time) {
	if c == nil || now == nil {
		return
	}
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

// SetEnabled — галочка «Запоминать разговоры»: выключена — ничего не пишем в архив и не даём модели память.
func (c *ChatThread) SetEnabled(f func() bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.enabled = f
	c.mu.Unlock()
}

// Enabled — память включена.
func (c *ChatThread) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	f := c.enabled
	c.mu.Unlock()
	return f == nil || f()
}

// SetSummarizer — чем сжимать старые реплики в досье, конспект и летопись (nil — не сжимать).
func (c *ChatThread) SetSummarizer(f Summarizer) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.summarize = f
	c.scheduleLocked()
	c.mu.Unlock()
}

func (c *ChatThread) archivePath() string {
	if c.path == "" {
		return ""
	}
	return strings.TrimSuffix(c.path, filepath.Ext(c.path)) + "-archive.jsonl"
}

func (c *ChatThread) loadArchive() {
	f, err := os.Open(c.archivePath())
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var t ChatTurn
		if json.Unmarshal(sc.Bytes(), &t) == nil && (t.User != "" || t.Bot != "") {
			c.archive = append(c.archive, t)
		}
	}
	sort.SliceStable(c.archive, func(i, j int) bool { return c.archive[i].N < c.archive[j].N })
}

func (c *ChatThread) rewriteArchive() {
	p := c.archivePath()
	if p == "" {
		return
	}
	var b strings.Builder
	for _, t := range c.archive {
		line, _ := json.Marshal(t)
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func (c *ChatThread) appendArchive(t ChatTurn) {
	if len(c.archive) > chatArchiveHard { // сжимать в летопись нечем (нет модели) — старое выкидываем, раз в несколько тысяч реплик
		drop := len(c.archive) - chatArchiveMax
		c.archive = append([]ChatTurn(nil), c.archive[drop:]...)
		c.rewriteArchive()
		log.Printf("память: архив переполнен, а сжать в летопись нечем — %d старых реплик удалено", drop)
		return
	}
	p := c.archivePath()
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	line, _ := json.Marshal(t)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func (c *ChatThread) saveLocked() {
	b, err := json.Marshal(c)
	if err != nil || c.path == "" {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// Thread — номер беседы для ChatGPT (один на все звонки).
func (c *ChatThread) Thread() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ID
}

// Add — реплика закончилась: запомнить вопрос и ответ.
func (c *ChatThread) Add(user, bot string) {
	user, bot = strings.TrimSpace(user), strings.TrimSpace(bot)
	if c == nil || (user == "" && bot == "") || !c.Enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := ChatTurn{N: c.Next, TS: c.now().Unix(), User: user, Bot: bot}
	if c.forgottenLocked(t) { // «забудь, что меня зовут Сергей» — ни сама просьба, ни ответ на неё в память не идут
		return
	}
	c.Next++
	c.archive = append(c.archive, t)
	c.appendArchive(t)
	c.saveLocked()
	c.scheduleLocked()
}

func (c *ChatThread) forgottenLocked(t ChatTurn) bool {
	now, keep := c.now(), c.forgets[:0]
	hit := false
	for _, f := range c.forgets {
		if now.After(f.until) {
			continue
		}
		keep = append(keep, f)
		if recallScore(t, f.terms) == len(f.terms) {
			hit = true
		}
	}
	c.forgets = keep
	return hit
}

// fromLocked — реплики с номером ≥ n (архив упорядочен по номерам).
func (c *ChatThread) fromLocked(n int64) []ChatTurn {
	i := sort.Search(len(c.archive), func(i int) bool { return c.archive[i].N >= n })
	return c.archive[i:]
}

// pendingLocked — реплики текущего чата, ещё не попавшие в конспект.
func (c *ChatThread) pendingLocked() []ChatTurn {
	return c.fromLocked(max(c.From, c.SummaryUpto+1))
}

// chatTail — последние реплики, всего не больше chars букв (но хотя бы одна).
func chatTail(turns []ChatTurn, chars int) []ChatTurn {
	n, i := 0, len(turns)
	for i > 0 {
		n += turns[i-1].size()
		if n > chars && i < len(turns) {
			break
		}
		i--
	}
	return turns[i:]
}

func chatSize(turns []ChatTurn) int {
	n := 0
	for _, t := range turns {
		n += t.size()
	}
	return n
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// Recent — последние реплики текущего чата, всего не больше chars букв (старые отбрасываются первыми).
func (c *ChatThread) Recent(chars int) []ChatTurn {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ChatTurn(nil), chatTail(c.fromLocked(c.From), chars)...)
}

// Transcript — последние реплики текущего чата дословно, по дням и со временем.
func (c *ChatThread) Transcript(chars int) string {
	if !c.Enabled() {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transcript(chatTail(c.fromLocked(c.From), chars), true)
}

// Context — память для начала звонка: досье, конспект и последние реплики дословно, всего около chars букв.
func (c *ChatThread) Context(chars int) string {
	if !c.Enabled() {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var parts []string
	if c.Profile != "" {
		parts = append(parts, "Что ты знаешь о хозяине (досье, копится всё время):\n"+c.Profile)
	}
	if c.Summary != "" {
		parts = append(parts, "Конспект недавних разговоров (когда, кто что сказал и сделал):\n"+c.Summary)
	}
	budget := max(chars-runes(c.Profile)-runes(c.Summary), chars/3)
	if recent := chatTail(c.pendingLocked(), budget); len(recent) > 0 {
		parts = append(parts, "Последние реплики дословно (время — у хозяина):\n"+c.transcript(recent, true))
	}
	if len(c.Chronicle) > 0 {
		parts = append(parts, fmt.Sprintf("Разговоры раньше этого сжаты в летопись: %s — %s (подробности — инструментом recall_conversation).",
			monthLabel(c.Chronicle[0].Month), monthLabel(c.Chronicle[len(c.Chronicle)-1].Month)))
	}
	return strings.Join(parts, "\n\n")
}

// HasArchive — есть ли что вспоминать инструментом.
func (c *ChatThread) HasArchive() bool {
	if !c.Enabled() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.archive) > 0 || len(c.Chronicle) > 0
}

var (
	ruMonthsGen = []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	ruMonths    = []string{"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"}
	ruWeekdays  = []string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
)

// monthLabel — «2026-11» → «ноябрь 2026».
func monthLabel(m string) string {
	t, err := time.Parse("2006-01", m)
	if err != nil {
		return m
	}
	return fmt.Sprintf("%s %d", ruMonths[t.Month()-1], t.Year())
}

// chatDay — «вчера, пятница 14 ноября»; relative=false — без «сегодня/вчера» (для памяти, она живёт долго).
func chatDay(t, now time.Time, relative bool) string {
	s := fmt.Sprintf("%s %d %s", ruWeekdays[t.Weekday()], t.Day(), ruMonthsGen[t.Month()-1])
	if t.Year() != now.Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	if relative {
		y, m, d := now.Date()
		ty, tm, td := t.Date()
		days := int(time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Sub(time.Date(ty, tm, td, 12, 0, 0, 0, time.UTC)).Hours() / 24)
		switch days {
		case 0:
			s = "сегодня, " + s
		case 1:
			s = "вчера, " + s
		case 2:
			s = "позавчера, " + s
		}
	}
	return s
}

// transcript — реплики по дням: «— вчера, пятница 14 ноября —», «18:05 Человек: …», «18:05 Джарвис: …».
// Пропуск между несоседними репликами — «…».
func (c *ChatThread) transcript(turns []ChatTurn, relative bool) string {
	now := c.now()
	var b strings.Builder
	day, prev := "", int64(0)
	for _, t := range turns {
		tm := time.Unix(t.TS, 0).In(now.Location())
		if d := chatDay(tm, now, relative); d != day {
			day = d
			b.WriteString("— " + d + " —\n")
		} else if prev != 0 && t.N > prev+1 {
			b.WriteString("…\n")
		}
		prev = t.N
		hm := tm.Format("15:04")
		if t.User != "" {
			b.WriteString(hm + " Человек: " + t.User + "\n")
		}
		if t.Bot != "" {
			b.WriteString(hm + " Джарвис: " + t.Bot + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------- обслуживание: правки, конспект, летопись

const memoryRules = "Ты ведёшь долгую память голосового ассистента Джарвиса в умной колонке хозяина — на годы: через год Джарвис должен знать хозяина как близкий друг. " +
	"«Человек» — хозяин (или кто-то в доме), «Джарвис» — ассистент; в квадратных скобках — что Джарвис сделал на колонке. " +
	"По-русски, коротко, только факты из реплик — ничего не выдумывай. Всегда пиши, кто что сказал или сделал: «хозяин рассказал…», " +
	"«хозяин просил…», «Джарвис посоветовал…». Даты — «14.11» (и время, если важно). Пропускай мелочи: громкость, свет, «спасибо», ошибки и повторы команд. " +
	"Факты с пометкой «хозяин сам попросил запомнить» важнее всего и заменяют противоречащие им. Без вступлений и пояснений.\n\n"

const compactInstructions = memoryRules +
	"Тебе дают досье на хозяина, прежний конспект и новые реплики по дням. Верни ЦЕЛИКОМ обновлённые досье и конспект — они заменят прежние — " +
	"строго в трёх разделах, каждый заголовок на отдельной строке:\nО ХОЗЯИНЕ:\nДОГОВОРЁННОСТИ:\nХРОНОЛОГИЯ:\n" +
	"- О ХОЗЯИНЕ — досье: имя, близкие и питомцы, работа и учёба, вкусы (музыка, еда, фильмы, игры), привычки и распорядок, " +
	"здоровье (если сам рассказал), дом и устройства, планы и мечты, как с ним лучше говорить. Перенеси всё из прежнего досье и добавь новое. " +
	"Не длиннее 2000 знаков: когда места мало, объединяй и обобщай, но устойчивое не теряй.\n" +
	"- ДОГОВОРЁННОСТИ — обещания, планы, напоминания с датами; прошедшее и выполненное убирай.\n" +
	"- ХРОНОЛОГИЯ — по дням: о чём говорили и что решили. Свежие дни подробнее, старше недели — строка на день, старше месяца — строка на неделю. " +
	"Не длиннее 2000 знаков."

const chronicleInstructions = memoryRules +
	"Тебе дают реплики за часть месяца (и прежнюю запись летописи за этот месяц, если она есть). Эти реплики уходят из архива навсегда, " +
	"поэтому сохрани всё ценное. Верни запись летописи за месяц ЦЕЛИКОМ: о чём говорили, что хозяин рассказал о себе и близких, " +
	"что решили и о чём договорились, что Джарвис советовал и делал — по датам. Не длиннее 1500 знаков."

const editInstructions = "Ты правишь долгую память голосового ассистента Джарвиса по просьбе хозяина. Тебе дают, что забыть и что запомнить, " +
	"и текущую память. Убери ВСЁ, что относится к забываемому (и прямые упоминания, и выводы из него), " +
	"впиши то, что надо запомнить, с пометкой «хозяин сам попросил запомнить», остальное оставь как было, слово в слово. " +
	"Верни память ЦЕЛИКОМ в том же виде, без пояснений."

// maintain — по очереди: правки «забудь/запомни», конспект, летопись. Один проход за раз.
func (c *ChatThread) maintain() {
	c.mu.Lock()
	if c.running {
		c.again = true
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	ctx := context.Background()
	for {
		c.applyEdits(ctx)
		_ = c.Compact(ctx)
		_ = c.Fold(ctx)
		c.mu.Lock()
		if !c.again {
			c.running = false
			c.mu.Unlock()
			return
		}
		c.again = false
		c.mu.Unlock()
	}
}

func (c *ChatThread) scheduleLocked() {
	if c.summarize == nil || c.compacting || c.now().Before(c.failUntil) || (c.enabled != nil && !c.enabled()) {
		return
	}
	if len(c.archive) > chatArchiveMax+200 {
		c.folding = true
	}
	if chatSize(c.pendingLocked()) < chatCompactAt && !(c.folding && len(c.archive) > chatArchiveMax) {
		c.folding = false
		return
	}
	if c.timer != nil {
		c.timer.Stop() // разговор ещё идёт — ждём паузу
	}
	c.timer = time.AfterFunc(chatCompactIdle, c.maintain)
}

func (c *ChatThread) failLocked(what string, err error) {
	c.failUntil = c.now().Add(10 * time.Minute)
	if !errors.Is(err, errNoSummarizer) {
		log.Printf("память: не получилось %s: %v", what, shortErr(err))
	}
}

// memorySections — ответ модели на три раздела: досье (О ХОЗЯИНЕ) и конспект (остальное). ok=false — разделов нет.
func memorySections(out string) (profile, summary string, ok bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	head := func(l string) string {
		u := strings.ToUpper(strings.Trim(strings.TrimSpace(l), "#*: "))
		switch {
		case strings.HasPrefix(u, "О ХОЗЯИНЕ"):
			return "p"
		case strings.HasPrefix(u, "ДОГОВОР"), strings.HasPrefix(u, "ХРОНОЛОГ"):
			return "s"
		}
		return ""
	}
	var p, s []string
	cur := ""
	for _, l := range lines {
		if h := head(l); h != "" && len([]rune(strings.TrimSpace(l))) < 40 {
			cur = h
			ok = ok || h == "p"
			if h == "s" {
				s = append(s, strings.Trim(strings.TrimSpace(l), "#* "))
			}
			continue
		}
		switch cur {
		case "p":
			p = append(p, l)
		case "s":
			s = append(s, l)
		}
	}
	if !ok {
		return "", strings.TrimSpace(out), false
	}
	return strings.TrimSpace(strings.Join(p, "\n")), strings.TrimSpace(strings.Join(s, "\n")), true
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Compact — дописать старые реплики (кроме последних chatCompactKeep букв) в досье и конспект.
func (c *ChatThread) Compact(ctx context.Context) error {
	_, err := c.compact(ctx, chatCompactKeep, chatCompactAt)
	return err
}

var errMemoryBusy = errors.New("память уже обновляется — подождите минуту")

// CompactNow — кнопка «Обновить досье сейчас»: сжать всё, кроме последней реплики, не дожидаясь длинного разговора.
func (c *ChatThread) CompactNow(ctx context.Context) (int, error) {
	if !c.Enabled() {
		return 0, errors.New("память выключена")
	}
	c.mu.Lock()
	f, busy := c.summarize, c.compacting
	c.mu.Unlock()
	if f == nil {
		return 0, errNoSummarizer
	}
	if busy {
		return 0, errMemoryBusy
	}
	return c.compact(ctx, 0, 1)
}

func (c *ChatThread) compact(ctx context.Context, keep, at int) (int, error) {
	c.mu.Lock()
	if c.summarize == nil || c.compacting {
		c.mu.Unlock()
		return 0, nil
	}
	pending := c.pendingLocked()
	old := pending[:len(pending)-len(chatTail(pending, keep))]
	n := 0
	for i, t := range old {
		if n += t.size(); n > chatCompactMax && i > 0 {
			old = old[:i]
			break
		}
	}
	if len(old) == 0 || chatSize(pending) < at {
		c.mu.Unlock()
		return 0, nil
	}
	old = append([]ChatTurn(nil), old...)
	c.compacting = true
	id, gen, rev, f := c.ID, c.gen, c.profileRev, c.summarize
	text := "Досье на хозяина (прежнее):\n" + or(c.Profile, "пока пусто") + "\n\nПрежний конспект:\n" + or(c.Summary, "пока пусто") +
		"\n\nНовые реплики (добавь их в досье и конспект):\n" + c.transcript(old, false)
	c.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	out, err := f(cctx, compactInstructions, text)
	cancel()
	if err == nil && strings.TrimSpace(out) == "" {
		err = errors.New("модель вернула пустой ответ")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.compacting = false
	if err != nil {
		c.failLocked("сжать разговор в конспект", err)
		return 0, err
	}
	if c.ID != id || c.gen != gen { // пока сжимали, начали новый чат или стёрли память
		return 0, nil
	}
	profile, summary, ok := memorySections(out)
	if ok && c.profileRev == rev { // «запомни» во время сжатия — досье не трогаем, учтём в следующий раз
		c.Profile = clip(profile, chatProfileMax*3/2)
	}
	c.Summary, c.SummaryUpto, c.SummaryAt = clip(summary, chatSummaryMax*3/2), old[len(old)-1].N, c.now().Unix()
	c.saveLocked()
	log.Printf("память: %d реплик дописано в досье и конспект (досье %d букв, конспект %d)", len(old), runes(c.Profile), runes(c.Summary))
	c.scheduleLocked() // осталось много — следующий проход
	return len(old), nil
}

func or(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// Fold — архив переполнен: самые старые реплики (одного месяца) сжать в летопись и убрать из архива.
func (c *ChatThread) Fold(ctx context.Context) error {
	c.mu.Lock()
	if c.summarize == nil || c.compacting || !c.folding || len(c.archive) <= chatArchiveMax {
		c.folding = c.folding && len(c.archive) > chatArchiveMax
		c.mu.Unlock()
		return nil
	}
	loc := c.now().Location()
	over := c.archive[:len(c.archive)-chatArchiveMax]
	month := time.Unix(over[0].TS, 0).In(loc).Format("2006-01")
	var batch []ChatTurn
	n := 0
	for _, t := range over {
		if t.N >= c.From && t.N > c.SummaryUpto { // ещё не в конспекте текущего чата — пусть сначала туда
			break
		}
		if time.Unix(t.TS, 0).In(loc).Format("2006-01") != month {
			break
		}
		if n += t.size(); n > chatCompactMax && len(batch) > 0 {
			break
		}
		batch = append(batch, t)
	}
	if len(batch) == 0 {
		c.mu.Unlock()
		return nil
	}
	c.compacting = true
	gen, f, prev := c.gen, c.summarize, ""
	for _, m := range c.Chronicle {
		if m.Month == month {
			prev = m.Text
		}
	}
	text := "Месяц: " + monthLabel(month) + "\n\nПрежняя запись летописи за этот месяц:\n" + or(prev, "пока нет") +
		"\n\nРеплики (добавь их в запись):\n" + c.transcript(batch, false)
	c.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	out, err := f(cctx, chronicleInstructions, text)
	cancel()
	out = strings.TrimSpace(out)
	if err == nil && out == "" {
		err = errors.New("модель вернула пустую запись")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.compacting = false
	if err != nil {
		c.failLocked("сжать старые реплики в летопись", err)
		return err
	}
	if c.gen != gen {
		return nil
	}
	c.setMonthLocked(month, clip(out, chatChronicleMax*3/2), len(batch))
	last := batch[len(batch)-1].N
	i := sort.Search(len(c.archive), func(i int) bool { return c.archive[i].N > last })
	c.archive = append([]ChatTurn(nil), c.archive[i:]...)
	c.rewriteArchive()
	c.saveLocked()
	log.Printf("память: %d старых реплик за %s ушли в летопись (%d букв)", len(batch), monthLabel(month), runes(out))
	c.scheduleLocked()
	return nil
}

func (c *ChatThread) setMonthLocked(month, text string, turns int) {
	for i := range c.Chronicle {
		if c.Chronicle[i].Month == month {
			c.Chronicle[i].Text, c.Chronicle[i].Turns = text, c.Chronicle[i].Turns+turns
			return
		}
	}
	c.Chronicle = append(c.Chronicle, ChronMonth{Month: month, Text: text, Turns: turns})
	sort.Slice(c.Chronicle, func(i, j int) bool { return c.Chronicle[i].Month < c.Chronicle[j].Month })
}

// ---------------------------------------------------------------- «забудь», «запомни», «меня зовут не Сергей, а Павел»

// EditMemory — инструмент edit_memory. Сразу: реплики про забываемое уходят из архива, новое — в досье.
// В фоне модель переписывает досье, конспект и летопись без забытого.
func (c *ChatThread) EditMemory(forget, remember string, words []string) string {
	forget, remember = strings.TrimSpace(forget), strings.TrimSpace(remember)
	if !c.Enabled() {
		return "Память выключена в настройках — запоминать и забывать нечего. Так и скажи."
	}
	if forget == "" && remember == "" {
		return "Скажи, что забыть или что запомнить."
	}
	c.mu.Lock()
	var out []string
	if remember != "" {
		c.Profile = strings.TrimSpace(c.Profile + "\n- " + remember + " (хозяин сам попросил запомнить, " + c.now().Format("02.01") + ")")
		c.profileRev++
		out = append(out, "Запомнил: «"+remember+"».")
	}
	if forget != "" {
		var terms []string
		for _, w := range words {
			terms = append(terms, recallTerms(w)...)
		}
		if len(terms) == 0 {
			terms = recallTerms(forget)
		}
		removed := 0
		if len(terms) > 0 {
			keep := make([]ChatTurn, 0, len(c.archive))
			for _, t := range c.archive {
				if recallScore(t, terms) == len(terms) {
					removed++
					continue
				}
				keep = append(keep, t)
			}
			if removed > 0 {
				c.archive = keep
				c.rewriteArchive()
			}
			c.forgets = append(c.forgets, forgetMark{terms, c.now().Add(chatForgetQuiet)})
		}
		out = append(out, fmt.Sprintf("Забываю «%s»: из архива удалено реплик — %d, досье, конспект и летопись перепишутся в течение минуты.", forget, removed))
		log.Printf("память: хозяин попросил забыть — удалено реплик из архива: %d", removed)
	}
	c.edits = append(c.edits, memEdit{forget, remember})
	c.saveLocked()
	c.mu.Unlock()
	go c.maintain()
	return strings.Join(out, " ") + " Коротко скажи хозяину, что сделал."
}

// applyEdits — переписать память по просьбам «забудь/запомни» (моделью; без неё — убрать строки со словами).
func (c *ChatThread) applyEdits(ctx context.Context) {
	c.mu.Lock()
	if len(c.edits) == 0 || c.compacting {
		c.mu.Unlock()
		return
	}
	edits := c.edits
	c.edits = nil
	f, gen := c.summarize, c.gen
	var task []string
	var terms [][]string
	for _, e := range edits {
		if e.Forget != "" {
			task = append(task, "Забыть: "+e.Forget)
			terms = append(terms, recallTerms(e.Forget))
		}
		if e.Remember != "" {
			task = append(task, "Запомнить: "+e.Remember)
		}
	}
	if f == nil { // модели нет — убираем строки, где есть все слова забываемого
		for _, t := range terms {
			c.Profile, c.Summary = dropLines(c.Profile, t), dropLines(c.Summary, t)
			for i := range c.Chronicle {
				c.Chronicle[i].Text = dropLines(c.Chronicle[i].Text, t)
			}
		}
		c.saveLocked()
		c.mu.Unlock()
		return
	}
	c.compacting = true
	rev, id := c.profileRev, c.ID
	mem := "О ХОЗЯИНЕ:\n" + or(c.Profile, "пока пусто") + "\n" + or(c.Summary, "ДОГОВОРЁННОСТИ:\nХРОНОЛОГИЯ:")
	months := map[string]string{}
	for _, m := range c.Chronicle {
		for _, t := range terms {
			if len(t) > 0 && recallScoreText(m.Text, t) > 0 {
				months[m.Month] = m.Text
			}
		}
	}
	c.mu.Unlock()

	ask := func(what string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return f(cctx, editInstructions, strings.Join(task, "\n")+"\n\nПамять:\n"+what)
	}
	out, err := ask(mem)
	newMonths := map[string]string{}
	if err == nil {
		for m, text := range months {
			if t, e := ask(text); e == nil && strings.TrimSpace(t) != "" {
				newMonths[m] = strings.TrimSpace(t)
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.compacting = false
	if c.gen != gen {
		return
	}
	if err != nil {
		c.edits = append(edits, c.edits...) // повторим позже
		c.failLocked("переписать память по просьбе хозяина", err)
		return
	}
	if profile, summary, ok := memorySections(out); ok {
		if c.profileRev == rev {
			c.Profile = clip(profile, chatProfileMax*3/2)
		}
		if c.ID == id {
			c.Summary = clip(summary, chatSummaryMax*3/2)
		}
	}
	for m, text := range newMonths {
		for i := range c.Chronicle {
			if c.Chronicle[i].Month == m {
				c.Chronicle[i].Text = clip(text, chatChronicleMax*3/2)
			}
		}
	}
	c.SummaryAt = c.now().Unix()
	c.saveLocked()
	log.Printf("память: переписана по просьбе хозяина (%d правок)", len(edits))
}

// dropLines — убрать строки, где есть все слова.
func dropLines(text string, terms []string) string {
	if len(terms) == 0 {
		return text
	}
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if recallScoreText(l, terms) < len(terms) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// ---------------------------------------------------------------- «вспомни»

// recallStop — слова, по которым искать бессмысленно: «что ты мне вчера говорил про…».
var recallStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("что как про мне мной меня тебе тебя это этот эта эти этом был была было были какой какая какое какие когда где там тут " +
		"все всё весь вся его её она они оно так уже ещё или для при над под без ним ней них кто чем чём нибудь либо тоже свой своя мой моя мои твой твоя твои " +
		"говорил говорила говорили сказал сказала сказали советовал советовала спрашивал спрашивала рассказывал рассказывала рассказал рассказала " +
		"вспомни вспомнить помнишь напомни разговор разговоре разговаривали обсуждали говорили беседа забудь забыть запомни запомнить " +
		"вчера сегодня позавчера утром днём днем вечером ночью неделе недавно прошлой прошлый раньше тогда давно " +
		"джарвис хозяин хозяина хозяину человек нас нам наш наша наши ваш день") {
		recallStop[w] = true
	}
}

// recallTerms — корни слов запроса: «пиццу» и «пицца» → «пицц».
func recallTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(strings.ReplaceAll(w, "ё", "е"))
		if len(r) < 3 || recallStop[w] {
			continue
		}
		if len(r) > 4 {
			r = r[:min(6, max(4, len(r)-2))]
		}
		if s := string(r); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func recallScore(t ChatTurn, terms []string) int { return recallScoreText(t.User+" "+t.Bot, terms) }

// recallScoreText — сколько корней из terms есть в тексте (как начало слова).
func recallScoreText(text string, terms []string) int {
	words := strings.FieldsFunc(strings.ReplaceAll(strings.ToLower(text), "ё", "е"), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	score := 0
	for _, term := range terms {
		for _, w := range words {
			if strings.HasPrefix(w, term) {
				score++
				break
			}
		}
	}
	return score
}

// recallDay — день из параметра date («2026-11-15») или из слов «сегодня/вчера/позавчера» в запросе.
func recallDay(date, q string, now time.Time) (time.Time, bool) {
	if d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), now.Location()); err == nil {
		return d, true
	}
	q = strings.ToLower(q + " " + date)
	switch {
	case strings.Contains(q, "позавчера"):
		return now.AddDate(0, 0, -2), true
	case strings.Contains(q, "вчера"):
		return now.AddDate(0, 0, -1), true
	case strings.Contains(q, "сегодня"):
		return now, true
	}
	return time.Time{}, false
}

// Search — найти в архиве реплики по словам и/или за день, а в летописи — месяцы по словам или по дню.
func (c *ChatThread) Search(query, date string) string {
	if !c.Enabled() {
		return "Память выключена в настройках: прошлых разговоров ты не помнишь. Так и скажи."
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.archive) == 0 && len(c.Chronicle) == 0 {
		return "Архив разговоров пуст — вспоминать нечего. Не выдумывай: скажи, что не помнишь."
	}
	now := c.now()
	loc := now.Location()
	day, byDay := recallDay(date, query, now)
	inDay := func(t ChatTurn) bool {
		if !byDay {
			return true
		}
		y, m, d := time.Unix(t.TS, 0).In(loc).Date()
		dy, dm, dd := day.Date()
		return y == dy && m == dm && d == dd
	}
	since := "летописи за " + monthLabel(firstMonth(c.Chronicle))
	if len(c.Chronicle) == 0 {
		since = chatDay(time.Unix(c.archive[0].TS, 0).In(loc), now, true)
	}
	terms := recallTerms(query)

	// летопись: месяцы по словам; за день старше архива — его месяц
	var chron []ChronMonth
	if len(terms) > 0 {
		type mh struct {
			m     ChronMonth
			score int
		}
		var ms []mh
		for _, m := range c.Chronicle {
			if s := recallScoreText(m.Text, terms); s > 0 {
				ms = append(ms, mh{m, s})
			}
		}
		sort.SliceStable(ms, func(a, b int) bool { return ms[a].score > ms[b].score })
		for i := 0; i < len(ms) && i < 2; i++ {
			chron = append(chron, ms[i].m)
		}
	}
	if byDay && (len(c.archive) == 0 || day.Unix() < c.archive[0].TS-86400) {
		for _, m := range c.Chronicle {
			if m.Month == day.Format("2006-01") && !hasMonth(chron, m.Month) {
				chron = append(chron, m)
			}
		}
	}

	type hit struct{ i, score int }
	var hits []hit
	var dayIdx []int
	for i, t := range c.archive {
		if !inDay(t) {
			continue
		}
		dayIdx = append(dayIdx, i)
		if len(terms) > 0 {
			if s := recallScore(t, terms); s > 0 {
				hits = append(hits, hit{i, s})
			}
		}
	}
	if len(chron) == 0 {
		if byDay && len(dayIdx) == 0 {
			return fmt.Sprintf("За %s в памяти разговоров нет (память начинается с %s). Не выдумывай.", chatDay(day, now, true), since)
		}
		if !byDay && len(hits) == 0 {
			return fmt.Sprintf("В памяти ничего не нашлось по запросу «%s» (она начинается с %s). Не выдумывай — скажи, что не помнишь, "+
				"или попробуй другие слова.", strings.TrimSpace(query), since)
		}
	}
	var pick []int
	if len(hits) > 0 {
		sort.SliceStable(hits, func(a, b int) bool {
			if hits[a].score != hits[b].score {
				return hits[a].score > hits[b].score
			}
			return hits[a].i > hits[b].i // при равенстве — свежее
		})
		for limit := min(8, len(hits)); limit >= 1; limit-- {
			set := map[int]bool{}
			for _, h := range hits[:limit] {
				for j := h.i - 1; j <= h.i+1; j++ { // соседние реплики — чтобы был понятен контекст
					if j >= 0 && j < len(c.archive) && inDay(c.archive[j]) {
						set[j] = true
					}
				}
			}
			pick = pick[:0]
			for j := range set {
				pick = append(pick, j)
			}
			sort.Ints(pick)
			if c.pickSize(pick) <= chatRecallMax {
				break
			}
		}
	} else if byDay {
		pick = dayIdx // весь день
	}
	var parts []string
	if len(pick) > 0 {
		turns := make([]ChatTurn, 0, len(pick))
		n, more := 0, 0
		for _, j := range pick {
			if n += c.archive[j].size(); n > chatRecallMax && len(turns) > 0 {
				more++
				continue
			}
			turns = append(turns, c.archive[j])
		}
		s := "Из архива разговоров (время — у хозяина; «Человек» — хозяин, «Джарвис» — ты; в скобках — что ты сделал):\n" + c.transcript(turns, true)
		if more > 0 {
			s += fmt.Sprintf("\n(и ещё %d реплик за этот день — уточни запрос словами, чтобы найти нужное)", more)
		}
		parts = append(parts, s)
	}
	for _, m := range chron {
		parts = append(parts, "Из летописи (старые разговоры, сжато) — "+monthLabel(m.Month)+":\n"+m.Text)
	}
	return strings.Join(parts, "\n\n")
}

func firstMonth(ms []ChronMonth) string {
	if len(ms) == 0 {
		return ""
	}
	return ms[0].Month
}

func hasMonth(ms []ChronMonth, m string) bool {
	for _, x := range ms {
		if x.Month == m {
			return true
		}
	}
	return false
}

func (c *ChatThread) pickSize(idx []int) int {
	n := 0
	for _, j := range idx {
		n += c.archive[j].size()
	}
	return n
}

// recallDefinition — инструмент «вспомнить» для всех моделей.
func recallDefinition() any {
	return map[string]any{"type": "function", "name": "recall_conversation",
		"description": "Вспомнить прошлые разговоры с хозяином: ищет по архиву всех разговоров на колонке и по летописи старых месяцев, " +
			"возвращает реплики дословно, с датой, временем и тем, кто что сказал. Вызывай сам, без просьбы «вспомни», когда хозяин ссылается " +
			"на прошлое («тот рецепт», «как я тебе рассказывал», «о чём говорили вчера», «помнишь…»), а в памяти выше этого нет. Не выдумывай прошлое — сначала поищи.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "о чём искать — ключевые слова: «пицца рецепт», «машина ремонт»; если нужен просто весь день — «весь день»"},
			"date":  map[string]any{"type": "string", "description": "день в формате ГГГГ-ММ-ДД, если хозяин назвал день («вчера», «в субботу» — посчитай дату сам); иначе пусто"}},
			"required": []any{"query"}}}
}

// memoryEditDefinition — инструмент «забудь / запомни / исправь».
func memoryEditDefinition() any {
	return map[string]any{"type": "function", "name": "edit_memory",
		"description": "Изменить долгую память о хозяине. Вызывай, когда хозяин просит что-то забыть («забудь, что…», «не запоминай это», " +
			"«сотри разговор про…»), исправить («меня зовут не Сергей, а Павел») или запомнить («запомни, что…»). " +
			"Исправление — это forget старого и remember нового в одном вызове.",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"forget":   map[string]any{"type": "string", "description": "что забыть, своими словами: «что хозяина зовут Сергей»; пусто — ничего не забывать"},
			"words":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "1–3 слова, по которым найти и удалить из архива реплики про забываемое, например «Сергей»; только слова, которые точно про это"},
			"remember": map[string]any{"type": "string", "description": "что запомнить или верное значение: «хозяина зовут Павел»; пусто — ничего"}}}}
}

// Reset — начать новый чат: конспект и недавние реплики забыть; досье, летопись и архив для «вспомни» остаются.
func (c *ChatThread) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.saveLocked()
}

func (c *ChatThread) resetLocked() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.ID, c.Since, c.From = newID(), time.Now().Unix(), c.Next
	c.Summary, c.SummaryUpto = "", 0
}

// Erase — стереть всю память: досье, летопись и архив.
func (c *ChatThread) Erase() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.gen++
	c.Profile, c.Chronicle, c.SummaryAt, c.archive, c.edits, c.forgets = "", nil, 0, nil, nil, nil
	if p := c.archivePath(); p != "" {
		_ = os.Remove(p)
	}
	c.saveLocked()
}

func (c *ChatThread) State() map[string]any {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	chron := []map[string]any{}
	for _, m := range c.Chronicle {
		chron = append(chron, map[string]any{"month": m.Month, "label": monthLabel(m.Month), "text": m.Text, "turns": m.Turns})
	}
	st := map[string]any{"turns": len(c.fromLocked(c.From)), "since": c.Since, "archive": len(c.archive),
		"profile": c.Profile, "summary": c.Summary, "summary_at": c.SummaryAt, "chronicle": chron}
	if len(c.archive) > 0 {
		st["archive_since"] = c.archive[0].TS
		st["recent"] = c.transcript(chatTail(c.archive, 6000), true) // последние реплики — посмотреть в панели
	}
	return st
}

// Лимит голоса в подписке ChatGPT («You have reached your usage limit»): когда он обновится, OpenAI не говорит,
// поэтому час не звоним в ChatGPT, а разговариваем через Gemini (если есть ключ). Потом пробуем снова.
var chatgptLimitedUntil atomic.Int64

const chatgptLimitRest = time.Hour

func chatgptLimited() bool { return time.Now().UnixNano() < chatgptLimitedUntil.Load() }

func chatgptLimitText(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "usage limit") || strings.Contains(m, "rate limit") || strings.Contains(m, "quota")
}

// AddFact — записать в чат, что Джарвис сделал сам (включил видео, выполнил команду): ChatGPT узнает это в следующем звонке.
func (c *ChatThread) AddFact(fact string) {
	if fact = strings.TrimSpace(fact); fact != "" {
		c.Add("", "["+fact+"]")
	}
}

// memoryWorthy — ответ без модели стоит запомнить: ошибки («недоступен», «не удалось») не запоминаем,
// иначе модель потом повторяет их вместо того, чтобы вызвать инструмент.
func memoryWorthy(answer string) bool {
	a := strings.ToLower(answer)
	for _, bad := range []string{"недоступ", "не подключ", "ошибк", "не удалось", "не получилось", "не отвеча", "не нашл",
		// модель назвала себя не Джарвисом — в память не берём, иначе следующий разговор повторит
		"я chatgpt", "я — chatgpt", "я gpt", "я gemini", "я — gemini", "я grok", "я языковая модель", "я большая языковая модель", "я — языковая модель"} {
		if strings.Contains(a, bad) {
			return false
		}
	}
	return true
}
