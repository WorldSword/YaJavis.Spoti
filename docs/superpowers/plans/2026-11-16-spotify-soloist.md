# Spotify (Soloist) в панели и голосе YaJavis — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Джарвис включает музыку в Spotify голосом («включи … в Spotify»), а веб-панель показывает и контролирует Spotify.

**Architecture:** Новый файл `jarvisd/spotify.go` — тонкая обёртка над уже установленным на колонке Soloist (запуск его скриптов и `soloist ctl … now --json` через `os/exec`). Spotify подключается как третий источник музыки рядом с `Music` (YouTube) и `AliceMusic`: `Engine.spotify`, `Tools.SpotifyPlay`, `MusicState.Source == "spotify"`. Код YouTube и Алисы не меняется.

**Tech Stack:** Go (модуль `jarvisd`, новых зависимостей нет), `jarvisd/ui/app.js`, тесты `package main` в стиле `musicrouting_test.go`.

**Spec:** `docs/superpowers/specs/2026-11-16-spotify-soloist-design.md`

## Global Constraints

- Никаких новых Go-зависимостей; без сети в тестах.
- Бинарники и библиотеки Soloist не попадают в репозиторий; Soloist только находится на колонке, из панели не ставится.
- Soloist ищется в `/data/yam/share/horohi.soloist`, затем в `/data/opt/soloist`; переопределение переменной окружения `SOLOIST_DIR`.
- Конфиги Soloist: YAM — `/data/yam/etc/horohi.soloist/config.env` (`API_KEY`, `DEVICE_NAME`) и `spotify_api.conf` (`SPOTIFY_CLIENT_ID`, `SPOTIFY_CLIENT_SECRET`); автономный — в корне установки.
- Запрос пользователя передаётся процессу одним аргументом, никогда через оболочку.
- Ключи не попадают в логи и отдаются панелью только через `mask()`.
- YouTube и музыку Алисы не удалять и не менять; кнопки колонки остаются за Джарвисом.
- Каждый коммит заканчивается строками:
  `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` и `Claude-Session: https://claude.ai/code/session_01T6XYHHrkSH8hAuNVsogimQ`.
- Проверка: `cd jarvisd && go vet ./... && go test ./...` (если Go-тулчейн 1.27.4 не скачивается без сети — сообщить владельцу, не обходить).

## Review Focus

1. Запрос с кавычками и метасимволами (`Don't Stop; rm -rf /`, `$(reboot)`) доходит до Soloist одним аргументом без оболочки — тест в Task 1.
2. Значения полей ключей с `"`, `$`, обратной кавычкой, переводом строки не попадают в `config.env`/`spotify_api.conf` (файлы читаются `sh`) — тест в Task 3.
3. Soloist не установлен: инструмент `play_spotify` не объявляется, панель показывает «не найден», вызовы не падают — тесты в Tasks 3 и 4.
4. `soloist ctl … now --json` пустой, невалидный, с подкастом (`spotify:episode:`) или без `item`: паники нет, `Active()` ложно или показан заголовок-заглушка — тест в Task 2.
5. Панель отправляет обратно маску вместо ключа (`mask(текущий)`) — ключ не затирается — тест в Task 3.

---

### Task 1: Обёртка над Soloist и разбор фразы

**Files:**
- Create: `jarvisd/spotify.go`
- Test: `jarvisd/spotify_test.go`

**Interfaces:**
- Produces:
  ```go
  type spotifyExec func(ctx context.Context, name string, args ...string) ([]byte, error)
  type Spotify struct { /* root string, exec spotifyExec, mu, cache; OnPlay func() */ }
  func NewSpotify() *Spotify                       // root из SOLOIST_DIR или findSoloistRoot; боевой exec на os/exec
  func newSpotifyAt(root string, exec spotifyExec) *Spotify
  func findSoloistRoot(candidates ...string) string // первый, где есть scripts/control.sh; "" если нет
  func (s *Spotify) Installed() bool
  func (s *Spotify) Root() string
  func (s *Spotify) Play(ctx context.Context, query string) error
  func (s *Spotify) Control(ctx context.Context, action string) error // pause|resume|next|prev|toggle
  func spotifyRequest(text string) (query string, ok bool)
  ```

- [ ] **Step 1: Write the failing tests** in `spotify_test.go`
  - `TestSpotifyRequest` (таблица, как `TestYoutubeRequest`): «Джарвис, включи в Spotify Linkin Park Numb» → `"Linkin Park Numb", true`; «поставь в спотифае Кино Группа крови» → `"Кино Группа крови", true`; «включи песню Don't Stop Me Now в спотике» → `"Don't Stop Me Now", true`; «включи в Spotify» → `"", true`; «Джарвис, включи музыку» → `"", false`; «поставь на паузу в спотифай» → `"", false` (это команда управления, её разбирает `musicCommand`).
  - `TestSpotifyPlayArgv`: фальшивый `exec` записывает вызов; `Play(ctx, "Don't Stop; rm -rf /")` вызывает `name == "sh"`, `args == []string{root+"/scripts/control.sh", "search", "Don't Stop; rm -rf /"}` (ровно один элемент на запрос).
  - `TestSpotifyPlayEmpty`: `Play(ctx, "  ")` возвращает ошибку, `exec` не вызван.
  - `TestSpotifyControlMapping`: `resume` → аргумент `play`; `pause`/`next`/`prev`/`toggle` без изменений; `"explode"` → ошибка, `exec` не вызван.
  - `TestFindSoloistRoot`: во временной папке с `scripts/control.sh` → путь, в пустой → `""`.
- [ ] **Step 2: Run** `cd jarvisd && go test -run 'TestSpotify|TestFindSoloist' ./...` — Expected: FAIL (символы не определены).
- [ ] **Step 3: Implement** всё из Interfaces в `spotify.go`. `spotifyRequest`: сначала отсекает команды, которые распознаёт `musicCommand`; находит слово Spotify/Спотифай/Спотик в любом падеже (регистр не важен), вырезает его с предлогом «в/на/через», глагол («включи/поставь/сыграй/запусти»), «песню/трек», «Джарвис», хвостовое «пожалуйста»; по образцу `youtubeRequest` (`music.go:1326`). Боевой `exec` — `exec.CommandContext`, таймаут из `ctx`, вывод `CombinedOutput`.
- [ ] **Step 4: Run** те же тесты — Expected: PASS.
- [ ] **Step 5: Commit** `feat(spotify): обёртка над Soloist и разбор голосовой фразы`

### Task 2: Состояние «что играет»

**Files:**
- Modify: `jarvisd/spotify.go`
- Test: `jarvisd/spotify_test.go`

**Interfaces:**
- Consumes: `Spotify.exec`, `Spotify.root` из Task 1.
- Produces:
  ```go
  type SpotifyNow struct { Playing bool; Title, Artist, Cover, URI string; Pos, Dur float64 }
  func parseSpotifyNow(b []byte) (SpotifyNow, error)
  func (s *Spotify) Now(ctx context.Context) (SpotifyNow, error) // <root>/bin/soloist ctl -w 127.0.0.1:9090 now --json, LD_LIBRARY_PATH=<root>/lib64
  func (s *Spotify) Active() bool    // последний успешный Now моложе 3 с и есть трек
  func (s *Spotify) Playing() bool
  func (s *Spotify) Paused() bool
  func (s *Spotify) Snapshot() MusicState // Source: "spotify"; Track{Title, Author}, Cover, Playing, Paused, Pos, Dur
  ```

> Форма JSON от Soloist подтверждена README только частично (`item.uri`, `context.uri`). Имена `is_playing`, `progress_ms`, `item.name`, `item.artists[].name`, `item.duration_ms`, `item.album.images[].url` в фикстурах — **предположение**; при первом запуске на колонке сверить с реальным выводом и поправить фикстуру и разбор вместе.

- [ ] **Step 1: Write the failing tests**
  - `TestParseSpotifyNow`: фикстура с предполагаемой формой → `Title`, `Artist`, `Cover`, `Dur` заполнены, `Playing` из `is_playing`.
  - `TestParseSpotifyNowDegraded`: `""`, `"not json"`, `{}`, `{"item":null}` → ошибка или `SpotifyNow{}` без паники; фикстура с `item.uri == "spotify:episode:abc"` и без `name` → `Title == "Spotify"`.
  - `TestSpotifyActiveExpires`: фальшивые часы/кэш: после `Now` с треком `Active()` истинно, через 4 с без обновления — ложно; ошибка `exec` → `Active()` ложно.
  - `TestSpotifySnapshot`: `Snapshot().Source == "spotify"`, `Paused` истинно, когда трек есть, а `Playing` ложно.
- [ ] **Step 2: Run** `go test -run 'ParseSpotifyNow|SpotifyActive|SpotifySnapshot' ./...` — Expected: FAIL.
- [ ] **Step 3: Implement** по Interfaces. Если прямой запуск `<root>/bin/soloist` не работает, повторить через `<root>/lib64/ld-linux-aarch64.so.1 --library-path <root>/lib64`, как в `scripts/status.sh` Soloist. Кэш обновляет фоновый опрос раз в 2 с, пока `Installed()` (запускается из `main.go` в Task 3).
- [ ] **Step 4: Run** те же тесты — Expected: PASS.
- [ ] **Step 5: Commit** `feat(spotify): состояние воспроизведения из soloist ctl`

### Task 3: Панель — бэкенд

**Files:**
- Create: `jarvisd/spotifyweb.go`, `jarvisd/spotifyweb_test.go`
- Modify: `jarvisd/engine.go` (поле `spotify *Spotify` в `Engine`), `jarvisd/main.go` (создать `NewSpotify()`, `engine.spotify = …`, запустить опрос), `jarvisd/web.go` (маршруты рядом с `/api/youtube`, поле `"spotify"` в ответе `state` рядом с `"youtube"`), `jarvisd/musicpanel.go` (`musicNow`, `postMusic`)

**Interfaces:**
- Consumes: `Spotify` из Tasks 1–2; `mask`, `readJSON`, `bad`, `writeJSON` из `web.go`.
- Produces:
  ```go
  func (w *Web) spotifyState() map[string]any  // installed, running, root, device_name, api_key, client_id, client_secret (маски)
  func (w *Web) putSpotify(rw http.ResponseWriter, r *http.Request)      // PUT  /api/spotify
  func (w *Web) spotifyRestart(rw http.ResponseWriter, r *http.Request)  // POST /api/spotify/restart
  func validSoloistValue(v string) bool   // ≤200 символов, без " \ $ ` и переводов строки
  func writeSoloistConfig(dir string, u spotifyConfigUpdate) error // 0600, запись через временный файл и rename
  ```

- [ ] **Step 1: Write the failing tests** (стиль `musicrouting_test.go`, временные папки вместо `/data`)
  - `TestSpotifyStateMasksKeys`: ключ `abcdef123456` в `config.env` → ответ содержит `mask(...)`, но не сам ключ.
  - `TestPutSpotifyWritesConfig`: PUT с новыми значениями → файлы созданы с правами `0600`, в `config.env` строки `API_KEY="…"` и `DEVICE_NAME="…"`.
  - `TestPutSpotifyMaskedKeepsKey`: PUT с `api_key == mask(текущий)` → файл не изменился.
  - `TestPutSpotifyRejectsShellChars`: значения `a"b`, `$(x)`, `` `x` ``, `a\nb` → 400, файлы не тронуты.
  - `TestSpotifyStateNotInstalled`: `Spotify` без root → `installed: false`, PUT возвращает 400 «Soloist не найден», без паники.
  - `TestMusicNowPrefersSpotify`: `Active()` истинно и YouTube не играет → `Source == "spotify"`; играет YouTube → `"youtube"`.
  - `TestPostMusicRoutesToSpotify`: действия `pause|resume|next|prev|stop` при активном Spotify вызывают `Control` с `pause|resume|next|prev|pause` (стоп у Soloist = пауза; `stop.sh` останавливает сервис, его не вызывать).
- [ ] **Step 2: Run** `go test -run 'SpotifyState|PutSpotify|MusicNowPrefers|PostMusicRoutes' ./...` — Expected: FAIL.
- [ ] **Step 3: Implement.** Перезапуск: YAM — `<yamRoot>/bin/soloist restart`; автономная установка — 400 «перезапустите Soloist вручную». `musicNow`/`postMusic`: ветка Spotify добавляется до веток Алисы, YouTube при `m.Playing()` остаётся приоритетным.
- [ ] **Step 4: Run** те же тесты, затем `go vet ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(spotify): бэкенд панели — состояние, ключи, перезапуск`

### Task 4: Голос

**Files:**
- Modify: `jarvisd/tools.go` (поле `SpotifyPlay`, определение `play_spotify` рядом с `play_music` ~строка 356, ветка в `Call` ~строка 516), `jarvisd/main.go` (присвоить `tools.SpotifyPlay`, образец — `tools.Music`, ~строка 580), `jarvisd/engine.go` (`controlSpotify`, `spotifyStarted`; вызов `spotifyRequest` до `youtubeRequest` на ~694; управление в ветке ~731), `jarvisd/textchat.go` (до `youtubeRequest` на ~128)
- Test: `jarvisd/spotifyvoice_test.go`

**Interfaces:**
- Consumes: `Spotify.Play/Control/Active/Playing`, `spotifyRequest` (Tasks 1–2); `Engine.spotify` (Task 3).
- Produces:
  ```go
  // Tools
  SpotifyPlay func(ctx context.Context, query string) string // nil — Soloist не найден, инструмент не объявляется
  func (e *Engine) controlSpotify(ctx context.Context, text string) (string, bool)
  func (e *Engine) spotifyStarted() // у Алисы играет своё — «пауза»; YouTube Джарвиса — Stop()
  ```

- [ ] **Step 1: Write the failing tests**
  - `TestPlaySpotifyToolDefined`: при `SpotifyPlay != nil` среди определений есть `play_spotify` с обязательным параметром `query`; при `nil` — нет.
  - `TestPlaySpotifyCall`: `Call(ctx, "play_spotify", `{"query":"Кино Группа крови"}`)` вызывает `SpotifyPlay` с этим запросом и возвращает его ответ; пустой запрос → «Не понял, что включить в Spotify.», `SpotifyPlay` не вызван.
  - `TestControlSpotify`: при активном Spotify «пауза» → `Control("pause")`, ответ «Пауза.»; «следующее» → `next`; «продолжи» → `resume`; «выключи музыку» → `pause`; «что играет» → ответ с названием и исполнителем из `Snapshot`; при неактивном Spotify — `("", false)`.
  - `TestSpotifyStartedStopsYouTube`: при играющем YouTube `spotifyStarted()` вызывает `m.Stop()`.
- [ ] **Step 2: Run** `go test -run 'PlaySpotify|ControlSpotify|SpotifyStarted' ./...` — Expected: FAIL.
- [ ] **Step 3: Implement.** Описание инструмента по-русски: включить трек, альбом или исполнителя в Spotify на колонке, вызывать на любые просьбы со словом Spotify/Спотифай/Спотик, запрос — исполнитель и название. Ответ `SpotifyPlay` — как у `tools.Music`: вне диалога «скажи вслух только «Включаю»» и `EndAfterMusic()`, в диалоге — фоном. `Spotify.OnPlay = engine.spotifyStarted`. `controlSpotify` вызывается в `controlMusicContext` первым и использует `musicCommand` для разбора. `Tools.SpotifyPlay` присваивается только при `spotify.Installed()`.
- [ ] **Step 4: Run** те же тесты и `go test ./...` — Expected: PASS, существующие тесты не сломаны.
- [ ] **Step 5: Commit** `feat(spotify): голосовой инструмент play_spotify и управление`

### Task 5: Интерфейс панели

**Files:**
- Modify: `jarvisd/ui/app.js` (подпись источника в `drawPlayer` ~строка 1085, карточка «Spotify» по образцу карточки YouTube, `musicAct`/`prev` для Spotify), при необходимости `jarvisd/ui/app.css`

**Interfaces:**
- Consumes: `GET /api/state` → `spotify{installed,running,device_name,api_key,client_id,client_secret}`; `PUT /api/spotify`; `POST /api/spotify/restart`; `GET/POST /api/music` с `source: "spotify"`.

- [ ] **Step 1: Implement** подпись «Spotify» в `drawPlayer`, пустое состояние «Скажите: Джарвис, включи … в Spotify», кнопка «назад» у Spotify активна (`prev` идёт в `POST /api/music`), карточка «Подключения → Spotify»: статус (найден/не найден, играет/нет), поля API-ключа Soloist, имени устройства, Client ID, Client Secret (маски как у соседних ключей), кнопки «Сохранить» и «Перезапустить Soloist».
- [ ] **Step 2: Verify** `node --check jarvisd/ui/app.js` — Expected: без вывода; `grep -n 'spotify' jarvisd/ui/app.js` показывает использование всех трёх эндпоинтов.
- [ ] **Step 3: Commit** `feat(spotify): карточка и плеер в панели`

### Task 6: Документация, проверка на колонке, патч

**Files:**
- Create: `docs/SPOTIFY.md`
- Modify: `README.md` (одна строка со ссылкой на `docs/SPOTIFY.md` в разделе «Панель управления»)

- [ ] **Step 1: Write** `docs/SPOTIFY.md`: что нужно (Premium, ключ Soloist на `developer.spotify.com/dashboard/soloist`, при голосовом поиске — приложение Spotify Developer), как поставить Soloist (`yam install horohi/soloist`, `soloist setup`), как ввести ключи в панели, голосовые фразы, и чек-лист приёмки на Станции 2:
  1. Soloist запускается, колонка видна в списке устройств Spotify.
  2. «Джарвис, включи X в Spotify» играет на колонке; в панели обложка/название.
  3. «пауза», «следующее», «что играет» управляют Spotify.
  4. Сенсорные кнопки: кто реагирует, не двойное ли нажатие.
  5. Команда Джарвиса Алисе (`alice_command`) не перехватывается мостом как запрос в Spotify.
  6. Вывод `soloist ctl -w 127.0.0.1:9090 now --json` приложить к отчёту — по нему сверяются фикстуры Task 2.
- [ ] **Step 2: Verify** `cd jarvisd && go vet ./... && go test ./...` — Expected: PASS.
- [ ] **Step 3: Commit** `docs(spotify): инструкция и чек-лист приёмки`
- [ ] **Step 4: Export** `git format-patch origin/HEAD..HEAD -o /home/claude/spotify-patch` и отправить владельцу патч с командой `git am`.
