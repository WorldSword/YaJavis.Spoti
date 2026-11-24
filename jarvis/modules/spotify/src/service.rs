use crate::parse::{Now, Track, parse_now, parse_queue, upcoming_after, valid_uri};
use jarvis_home_common::{Callback, text};
use serde_json::{Value, json};
use std::{
    future::Future,
    path::PathBuf,
    pin::Pin,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    time::{Duration, Instant},
};
use tokio::sync::Notify;

/// Плейлист «DJ» Spotify: один на всех.
const DJ_URI: &str = "spotify:playlist:37i9dQZF1EYkqdzj48dyYq";
const NOT_FOUND: &str = "Soloist не найден на колонке";
/// Как Spotify называет плейлист «Любимые треки» в разных языках.
const LIKED_NAMES: [&str; 5] = ["liked songs", "любимые треки", "понравившиеся", "избранное", "любимые песни"];

pub type RunFuture<'a> = Pin<Box<dyn Future<Output = Result<String, String>> + Send + 'a>>;

/// Команда Soloist или колонки; её исполняет Runner (в тестах подменяется).
#[derive(Clone, Debug, PartialEq)]
pub enum Cmd {
    /// `soloist ctl -w 127.0.0.1:9090 <args>`
    Ctl(Vec<String>),
    /// `scripts/control.sh <args>` из комплекта Soloist
    Control(Vec<String>),
    /// громкость колонки, проценты
    Volume(u8),
}

pub trait Runner: Send + Sync {
    fn installed(&self) -> bool;
    fn run(&self, cmd: Cmd) -> RunFuture<'_>;
}

/// Настоящий Soloist на колонке.
pub struct Soloist {
    root: PathBuf,
}

impl Soloist {
    /// Первая папка, где лежит scripts/control.sh; None, если Soloist не найден.
    pub fn find(extra: Option<&str>) -> Option<Self> {
        let mut roots: Vec<PathBuf> = extra.filter(|d| !d.is_empty()).map(PathBuf::from).into_iter().collect();
        roots.push("/data/yam/share/horohi.soloist".into());
        roots.push("/data/opt/soloist".into());
        roots
            .into_iter()
            .find(|r| r.join("scripts/control.sh").is_file())
            .map(|root| Self { root })
    }

    async fn exec(program: &str, args: &[String], limit: Duration) -> Result<String, String> {
        let mut command = tokio::process::Command::new(program);
        // Ядро запускает плагин с пустым окружением, а скриптам Soloist нужен PATH.
        command
            .args(args)
            .env("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/system/bin:/system/xbin")
            .kill_on_drop(true)
            .stdin(std::process::Stdio::null());
        let out = tokio::time::timeout(limit, command.output())
            .await
            .map_err(|_| format!("{program}: не ответил за {} с", limit.as_secs()))?
            .map_err(|e| format!("{program}: {e}"))?;
        let mut all = String::from_utf8_lossy(&out.stdout).into_owned();
        all.push_str(&String::from_utf8_lossy(&out.stderr));
        if out.status.success() {
            return Ok(all);
        }
        let last = all.trim().lines().last().unwrap_or_default().chars().take(160).collect::<String>();
        Err(if last.is_empty() { format!("{program}: {}", out.status) } else { format!("{program}: {} ({last})", out.status) })
    }

    async fn ctl(&self, args: &[String], limit: Duration) -> Result<String, String> {
        let bin = self.root.join("bin/soloist");
        let lib = self.root.join("lib64");
        let env = format!("LD_LIBRARY_PATH={}", lib.display());
        let tail = ["ctl".to_string(), "-w".into(), "127.0.0.1:9090".into()];
        let direct: Vec<String> =
            [vec![env.clone(), bin.display().to_string()], tail.to_vec(), args.to_vec()].concat();
        let first = Self::exec("env", &direct, limit).await;
        // Если системный загрузчик не подходит — запасной запуск через загрузчик из lib64 (как в scripts/status.sh Soloist).
        let loader = lib.join("ld-linux-aarch64.so.1");
        if first.is_err() && loader.is_file() {
            let via: Vec<String> = [
                vec![env, loader.display().to_string(), "--library-path".into(), lib.display().to_string()],
                vec!["--argv0".into(), bin.display().to_string(), bin.display().to_string()],
                tail.to_vec(),
                args.to_vec(),
            ]
            .concat();
            return Self::exec("env", &via, limit).await;
        }
        first
    }
}

impl Runner for Soloist {
    fn installed(&self) -> bool {
        true
    }
    fn run(&self, cmd: Cmd) -> RunFuture<'_> {
        Box::pin(async move {
            match cmd {
                Cmd::Ctl(args) => {
                    let limit = if args.first().map(String::as_str) == Some("play") { 20 } else { 5 };
                    self.ctl(&args, Duration::from_secs(limit)).await
                }
                Cmd::Control(args) => {
                    let script = self.root.join("scripts/control.sh").display().to_string();
                    Self::exec("sh", &[vec![script], args].concat(), Duration::from_secs(40)).await
                }
                Cmd::Volume(v) => {
                    Self::exec("amixer", &["sset".into(), "Master".into(), format!("{v}%")], Duration::from_secs(3)).await
                }
            }
        })
    }
}

#[derive(Default)]
struct Inner {
    last: Option<(Now, Instant)>,
    next: Vec<Track>,
    next_for: String,
    liked: String,
    volume_seen: Option<u8>,
    error: String,
    published: Value,
}

#[derive(Default)]
struct Gate {
    /// сколько ответов Джарвиса сейчас говорит
    n: usize,
    /// мы поставили Spotify на паузу и должны вернуть
    armed: bool,
    /// счётчик команд Soloist сразу после нашей паузы
    seq: u64,
    generation: u64,
}

pub struct Spotify {
    runner: Arc<dyn Runner>,
    services: Callback,
    inner: Mutex<Inner>,
    gate: Mutex<Gate>,
    cmds: AtomicU64,
    wake: Notify,
    closed: AtomicBool,
    resume_delay: Mutex<Duration>,
}

impl Spotify {
    pub fn new(runner: Arc<dyn Runner>, services: Callback) -> Arc<Self> {
        Arc::new(Self {
            runner,
            services,
            inner: Mutex::default(),
            gate: Mutex::default(),
            cmds: AtomicU64::new(0),
            wake: Notify::new(),
            closed: AtomicBool::new(false),
            resume_delay: Mutex::new(Duration::from_secs(4)),
        })
    }

    pub fn set_resume_delay(&self, delay: Duration) {
        *self.resume_delay.lock().unwrap() = delay;
    }

    fn installed(&self) -> Result<(), String> {
        if self.runner.installed() { Ok(()) } else { Err(NOT_FOUND.into()) }
    }

    async fn control(&self, args: &[&str]) -> Result<String, String> {
        self.installed()?;
        self.cmds.fetch_add(1, Ordering::SeqCst);
        self.runner.run(Cmd::Control(args.iter().map(|s| s.to_string()).collect())).await
    }

    async fn ctl(&self, args: &[&str]) -> Result<String, String> {
        self.installed()?;
        self.runner.run(Cmd::Ctl(args.iter().map(|s| s.to_string()).collect())).await
    }

    fn active_now(&self) -> Option<Now> {
        let inner = self.inner.lock().unwrap();
        let (now, at) = inner.last.as_ref()?;
        (!now.uri.is_empty() && at.elapsed() <= Duration::from_secs(6)).then(|| now.clone())
    }

    /// Спросить Soloist, что играет, и запомнить ответ; громкость из приложения переносим на колонку.
    pub async fn poll(&self) {
        if !self.runner.installed() {
            return;
        }
        let parsed = match self.runner.run(Cmd::Ctl(vec!["now".into(), "--json".into()])).await {
            Ok(out) => parse_now(&out),
            Err(e) => Err(e),
        };
        let mut volume = None;
        let mut queue_for = None;
        {
            let mut inner = self.inner.lock().unwrap();
            match parsed {
                Err(e) => {
                    inner.last = None;
                    inner.error = e;
                    inner.next.clear();
                    inner.next_for.clear();
                    return;
                }
                Ok(now) => {
                    inner.error.clear();
                    if let Some(v) = now.volume {
                        let prev = inner.volume_seen.replace(v);
                        // первое значение только запоминаем: при запуске громкость колонки не трогаем
                        if prev.is_some_and(|p| p != v) {
                            volume = Some(v);
                        }
                    }
                    let name = now.context_name.trim().to_lowercase();
                    if !now.context_uri.is_empty() && LIKED_NAMES.contains(&name.as_str()) {
                        inner.liked = now.context_uri.clone();
                    }
                    if !now.uri.is_empty() && inner.next_for != now.uri {
                        queue_for = Some(now.uri.clone());
                    }
                    inner.last = Some((now, Instant::now()));
                }
            }
        }
        if let Some(v) = volume
            && let Err(e) = self.runner.run(Cmd::Volume(v)).await
        {
            log::warn!("spotify: громкость {v}% не применилась: {e}");
        }
        if let Some(uri) = queue_for {
            let queue = match self.runner.run(Cmd::Ctl(vec!["queue".into(), "--json".into()])).await {
                Ok(out) => parse_queue(&out),
                Err(e) => Err(e),
            };
            let mut inner = self.inner.lock().unwrap();
            match queue {
                Ok(q) => (inner.next, inner.next_for) = (q, uri),
                Err(_) => inner.next.clear(),
            }
        }
    }

    pub fn current(&self) -> Value {
        let now = self.active_now();
        let (playing, track, pos, cover) = match &now {
            Some(n) => (n.playing, Track { id: n.uri.clone(), title: n.title.clone(), author: n.artist.clone(), art: n.cover.clone() }, n.pos, n.cover.clone()),
            None => (false, Track::default(), 0.0, String::new()),
        };
        let status = match &now {
            Some(n) if n.playing => format!("Играет «{}»", n.title),
            Some(n) => format!("На паузе «{}»", n.title),
            None => {
                let error = self.inner.lock().unwrap().error.clone();
                if error.is_empty() { "Сейчас ничего не играет".into() } else { error }
            }
        };
        json!({
            "playing":playing, "active":now.is_some(), "paused":now.is_some() && !playing,
            "proposal":false, "personal_search":false, "repeat":false,
            "cur":track, "cover":cover, "pos":(pos*10.0).round()/10.0, "status":status
        })
    }

    pub fn snapshot(&self) -> Value {
        let now = self.active_now();
        let inner = self.inner.lock().unwrap();
        let next = now.as_ref().map(|n| upcoming_after(&inner.next, &n.uri)).unwrap_or_default();
        let (playing, track, cover, pos, dur) = match &now {
            Some(n) => (n.playing, Track { id: n.uri.clone(), title: n.title.clone(), author: n.artist.clone(), art: n.cover.clone() }, n.cover.clone(), n.pos, n.dur),
            None => (false, Track::default(), String::new(), 0.0, 0.0),
        };
        json!({
            "source":"spotify", "playing":playing, "paused":now.is_some() && !playing, "loading":false,
            "track":track, "cover":cover, "pos":pos, "dur":dur, "next":next, "queue":next.len(),
            "radio":false, "repeat":false, "history":[], "error":inner.error
        })
    }

    async fn emit(&self, method: &str, value: Value) {
        let _ = (self.services)(method.into(), value).await;
    }

    /// Один шаг опроса: спросить Soloist и сообщить ядру, если состояние изменилось.
    pub async fn tick(&self) {
        self.poll().await;
        let mut state = self.current();
        state["pos"] = state["pos"].as_f64().unwrap_or_default().floor().into();
        let previous = std::mem::replace(&mut self.inner.lock().unwrap().published, state.clone());
        if state != previous {
            if state["playing"] == true && previous["playing"] != true {
                self.emit("music.play", Value::Null).await;
            }
            self.emit("music.state", state).await;
        }
    }

    pub fn watch(self: &Arc<Self>) {
        let weak = Arc::downgrade(self);
        tokio::spawn(async move {
            loop {
                let Some(service) = weak.upgrade() else { break };
                if service.closed.load(Ordering::Acquire) {
                    break;
                }
                service.tick().await;
                let idle = service.active_now().is_none_or(|n| !n.playing);
                let wait = Duration::from_secs(if idle { 3 } else { 2 });
                tokio::select! {
                    _ = service.wake.notified() => {}
                    _ = tokio::time::sleep(wait) => {}
                }
            }
        });
    }

    pub fn stop(&self) {
        self.closed.store(true, Ordering::Release);
        self.wake.notify_one();
    }

    /// Вход протокола музыки: `{op,args}` → `{value,state}` или `{error,state}`.
    pub async fn music(self: &Arc<Self>, input: Value) -> Result<Value, String> {
        let op = input["op"].as_str().unwrap_or_default().to_string();
        let result = self.command(&op, &input["args"]).await;
        let state = self.current();
        Ok(match result {
            Ok(value) => json!({"value":value,"state":state}),
            Err(error) => json!({"error":error,"state":state}),
        })
    }

    async fn command(self: &Arc<Self>, op: &str, args: &Value) -> Result<Value, String> {
        match op {
            // очередью Spotify управляет сам; панель шлёт это перед включением трека из очереди
            "state" | "unqueue" => Ok(Value::Null),
            "snapshot" => Ok(self.snapshot()),
            "play" => {
                let query = text(&args["query"]);
                let query = query.trim();
                if query.is_empty() {
                    return Err("что включить?".into());
                }
                self.control(&["search", query]).await?;
                self.settle().await;
                Ok(Value::Null)
            }
            "play_id" => {
                self.play_uri(&text(&args["id"])).await?;
                Ok(Value::Null)
            }
            "pause" | "stop" => self.transport("pause").await,
            "resume" => self.transport("play").await,
            "next" => self.transport("next").await,
            "prev" => self.transport("prev").await,
            "toggle" => self.transport("toggle").await,
            "seek" => {
                let pos = if let Some(pos) = args["pos"].as_f64() {
                    pos
                } else {
                    // ядро шлёт {delta, abs}: абсолютная позиция или сдвиг от текущей
                    let delta = args["delta"].as_f64().unwrap_or(0.0);
                    if args["abs"] == true {
                        delta
                    } else {
                        let current = self.active_now().map_or(0.0, |n| n.pos);
                        (current + delta).max(0.0)
                    }
                };
                if !(0.0..=24.0 * 3600.0).contains(&pos) {
                    return Err("недопустимая позиция".into());
                }
                self.ctl(&["seek", &((pos * 1000.0) as i64).to_string()]).await?;
                self.settle().await;
                Ok(Value::Null)
            }
            "play_dj" => {
                self.play_uri(DJ_URI).await?;
                Ok(Value::Null)
            }
            "radio" => {
                let uri = self.inner.lock().unwrap().last.as_ref().map(|(n, _)| n.uri.clone()).unwrap_or_default();
                match uri.strip_prefix("spotify:track:").filter(|id| !id.is_empty()) {
                    Some(id) => self.play_uri(&format!("spotify:station:track:{id}")).await?,
                    None => return Err("сейчас не играет трек, по которому можно включить радио".into()),
                }
                Ok(Value::Null)
            }
            "play_liked" => {
                let liked = self.inner.lock().unwrap().liked.clone();
                if liked.is_empty() {
                    return Err("не знаю, где ваши любимые треки: включите их один раз в приложении Spotify на эту колонку, дальше Джарвис запомнит".into());
                }
                self.play_uri(&liked).await?;
                Ok(Value::Null)
            }
            "voice_start" => {
                self.voice_start().await;
                Ok(Value::Null)
            }
            "voice_end" => {
                self.voice_end();
                Ok(Value::Null)
            }
            other => Err(format!("неизвестная операция Spotify: {other}")),
        }
    }

    async fn transport(&self, arg: &str) -> Result<Value, String> {
        self.control(&[arg]).await?;
        self.settle().await;
        Ok(Value::Null)
    }

    async fn play_uri(&self, uri: &str) -> Result<(), String> {
        self.installed()?;
        if !valid_uri(uri) {
            return Err("недопустимый адрес Spotify".into());
        }
        self.cmds.fetch_add(1, Ordering::SeqCst);
        self.ctl(&["play", uri]).await.map_err(|e| format!("Soloist не включил: {e}"))?;
        self.settle().await;
        Ok(())
    }

    /// После команды сразу перечитываем состояние, чтобы панель не ждала следующего опроса.
    async fn settle(&self) {
        self.poll().await;
        self.wake.notify_one();
    }

    // Soloist занимает звуковое устройство сам, и голос Джарвиса при играющем Spotify не слышен.
    // На время голоса ставим Spotify на паузу и потом возвращаем, но только если никто не вмешался:
    // человек не сказал «пауза», не включилось другое.
    async fn voice_start(&self) {
        let need = {
            let mut gate = self.gate.lock().unwrap();
            gate.n += 1;
            gate.generation += 1;
            !gate.armed && self.active_now().is_some_and(|n| n.playing)
        };
        if !need || self.control(&["pause"]).await.is_err() {
            return;
        }
        self.poll().await;
        let mut gate = self.gate.lock().unwrap();
        gate.armed = true;
        gate.seq = self.cmds.load(Ordering::SeqCst);
    }

    fn voice_end(self: &Arc<Self>) {
        let generation = {
            let mut gate = self.gate.lock().unwrap();
            gate.n = gate.n.saturating_sub(1);
            if gate.n > 0 || !gate.armed {
                return;
            }
            gate.generation += 1;
            gate.generation
        };
        let this = self.clone();
        let delay = *self.resume_delay.lock().unwrap();
        tokio::spawn(async move {
            tokio::time::sleep(delay).await;
            let seq = {
                let mut gate = this.gate.lock().unwrap();
                if gate.generation != generation || gate.n > 0 || !gate.armed {
                    return;
                }
                gate.armed = false;
                gate.seq
            };
            let paused = this.active_now().is_some_and(|n| !n.playing);
            if this.cmds.load(Ordering::SeqCst) != seq || !paused {
                return;
            }
            if let Err(e) = this.control(&["play"]).await {
                log::warn!("spotify: возврат музыки после голоса Джарвиса: {e}");
            }
            this.settle().await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{Value, json};
    use std::{
        sync::{Arc, Mutex},
        time::Duration,
    };

    const NOW: &str = include_str!("../tests/fixtures/soloist_now_dj.json");
    const QUEUE: &str = include_str!("../tests/fixtures/soloist_queue.json");

    /// Soloist с состоянием: pause/play меняют, играет он или нет.
    struct World {
        playing: Mutex<bool>,
        volume: Mutex<u8>,
        context_name: Mutex<String>,
        log: Mutex<Vec<String>>,
    }
    impl World {
        fn new(playing: bool) -> Arc<Self> {
            Arc::new(Self {
                playing: Mutex::new(playing),
                volume: Mutex::new(50),
                context_name: Mutex::new("DJ".into()),
                log: Mutex::default(),
            })
        }
        fn log(&self) -> Vec<String> {
            self.log.lock().unwrap().clone()
        }
        fn count(&self, what: &str) -> usize {
            self.log().iter().filter(|c| c.as_str() == what).count()
        }
    }
    impl Runner for World {
        fn installed(&self) -> bool {
            true
        }
        fn run(&self, cmd: Cmd) -> RunFuture<'_> {
            let line = match &cmd {
                Cmd::Ctl(a) => format!("ctl {}", a.join(" ")),
                Cmd::Control(a) => format!("control {}", a.join(" ")),
                Cmd::Volume(v) => format!("volume {v}"),
            };
            self.log.lock().unwrap().push(line.clone());
            let out = match line.as_str() {
                "ctl now --json" => {
                    let mut s = NOW.replace(
                        "\"status\":\"playing\"",
                        if *self.playing.lock().unwrap() { "\"status\":\"playing\"" } else { "\"status\":\"paused\"" },
                    );
                    s = s.replace("\"volume\":50", &format!("\"volume\":{}", self.volume.lock().unwrap()));
                    s.replace("\"name\":\"DJ\"", &format!("\"name\":\"{}\"", self.context_name.lock().unwrap()))
                }
                "ctl queue --json" => QUEUE.to_string(),
                "control pause" => {
                    *self.playing.lock().unwrap() = false;
                    String::new()
                }
                "control play" | "control next" => {
                    *self.playing.lock().unwrap() = true;
                    String::new()
                }
                _ => String::new(),
            };
            Box::pin(async move { Ok(out) })
        }
    }

    fn service(world: &Arc<World>) -> Arc<Spotify> {
        let sp = Spotify::new(world.clone(), Arc::new(|_, _| Box::pin(async { Ok(json!({})) })));
        sp.set_resume_delay(Duration::from_millis(30));
        sp
    }
    async fn op(sp: &Arc<Spotify>, name: &str, args: Value) -> Value {
        sp.music(json!({"op":name,"args":args})).await.unwrap()
    }

    #[tokio::test]
    async fn poll_publishes_state() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await;
        let s = op(&sp, "state", Value::Null).await["state"].clone();
        assert_eq!(s["playing"], true);
        assert_eq!(s["active"], true);
        assert_eq!(s["cur"]["Title"], "SWIM");
        assert_eq!(s["cur"]["Author"], "BTS");
        assert_eq!(s["status"], "Играет «SWIM»");
    }

    #[tokio::test]
    async fn idle_before_first_poll() {
        let w = World::new(true);
        let sp = service(&w);
        let s = op(&sp, "state", Value::Null).await["state"].clone();
        assert_eq!(s["active"], false);
        assert_eq!(s["status"], "Сейчас ничего не играет");
    }

    #[tokio::test]
    async fn queue_is_fetched_only_when_track_changes() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await;
        sp.poll().await;
        assert_eq!(w.count("ctl queue --json"), 1);
        let snap = op(&sp, "snapshot", Value::Null).await["value"].clone();
        assert_eq!(snap["source"], "spotify");
        assert_eq!(snap["track"]["Title"], "SWIM");
        assert_eq!(snap["next"][0]["Title"], "так похуй");
        assert_eq!(snap["cover"].as_str().unwrap().contains("00001e02"), true);
    }

    #[tokio::test]
    async fn volume_change_in_spotify_moves_speaker_but_first_value_does_not() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await;
        assert_eq!(w.count("volume 50"), 0);
        *w.volume.lock().unwrap() = 30;
        sp.poll().await;
        assert_eq!(w.count("volume 30"), 1);
        sp.poll().await;
        assert_eq!(w.count("volume 30"), 1);
    }

    #[tokio::test]
    async fn transport_ops_map_to_control_script() {
        let w = World::new(true);
        let sp = service(&w);
        for (name, cmd) in [("pause", "control pause"), ("resume", "control play"), ("next", "control next"), ("prev", "control prev")] {
            let r = op(&sp, name, Value::Null).await;
            assert!(r.get("error").is_none(), "{name}: {r}");
            assert!(w.log().contains(&cmd.to_string()), "{name}");
        }
        let r = op(&sp, "explode", Value::Null).await;
        assert!(r["error"].as_str().unwrap().contains("explode"));
    }

    #[tokio::test]
    async fn seek_understands_core_delta_and_abs() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await; // pos = 79.643
        op(&sp, "seek", json!({"delta":0,"abs":true})).await;
        assert!(w.log().contains(&"ctl seek 0".to_string()));
        op(&sp, "seek", json!({"delta":10,"abs":false})).await;
        assert!(w.log().contains(&"ctl seek 89643".to_string()), "{:?}", w.log());
        op(&sp, "seek", json!({"delta":-1000,"abs":false})).await;
        assert!(w.log().contains(&"ctl seek 0".to_string()));
    }

    #[tokio::test]
    async fn play_searches_by_query_and_rejects_empty() {
        let w = World::new(false);
        let sp = service(&w);
        let r = op(&sp, "play", json!({"query":"  Linkin Park "})).await;
        assert!(r.get("error").is_none());
        assert!(w.log().contains(&"control search Linkin Park".to_string()));
        assert!(op(&sp, "play", json!({"query":" "})).await["error"].is_string());
    }

    #[tokio::test]
    async fn unqueue_is_accepted_but_changes_nothing() {
        let w = World::new(true);
        let sp = service(&w);
        let r = op(&sp, "unqueue", json!({"index":0})).await;
        assert!(r.get("error").is_none(), "{r}");
        assert!(w.log().is_empty());
    }

    #[tokio::test]
    async fn play_id_validates_uri() {
        let w = World::new(false);
        let sp = service(&w);
        assert!(op(&sp, "play_id", json!({"id":"spotify:track:a;rm -rf"})).await["error"].is_string());
        let r = op(&sp, "play_id", json!({"id":"spotify:track:abc123"})).await;
        assert!(r.get("error").is_none());
        assert!(w.log().contains(&"ctl play spotify:track:abc123".to_string()));
    }

    #[tokio::test]
    async fn dj_and_radio_and_liked() {
        let w = World::new(true);
        let sp = service(&w);
        assert!(op(&sp, "radio", Value::Null).await["error"].is_string()); // ещё не опрашивали
        op(&sp, "play_dj", Value::Null).await;
        assert!(w.log().contains(&"ctl play spotify:playlist:37i9dQZF1EYkqdzj48dyYq".to_string()));
        sp.poll().await;
        op(&sp, "radio", Value::Null).await;
        assert!(w.log().contains(&"ctl play spotify:station:track:68lbSrXDORS51pmyjZv712".to_string()));
        assert!(op(&sp, "play_liked", Value::Null).await["error"].is_string());
        *w.context_name.lock().unwrap() = "Liked Songs".into();
        sp.poll().await;
        op(&sp, "play_liked", Value::Null).await;
        assert_eq!(w.count("ctl play spotify:playlist:37i9dQZF1EYkqdzj48dyYq"), 2);
    }

    #[tokio::test]
    async fn seek_in_milliseconds_and_bounds() {
        let w = World::new(true);
        let sp = service(&w);
        op(&sp, "seek", json!({"pos":12.5})).await;
        assert!(w.log().contains(&"ctl seek 12500".to_string()));
        assert!(op(&sp, "seek", json!({"pos":-1})).await["error"].is_string());
    }

    #[tokio::test]
    async fn voice_pauses_playing_music_and_brings_it_back() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await;
        op(&sp, "voice_start", Value::Null).await;
        assert!(!*w.playing.lock().unwrap());
        op(&sp, "voice_end", Value::Null).await;
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert!(*w.playing.lock().unwrap());
    }

    #[tokio::test]
    async fn voice_does_not_touch_music_that_was_not_playing() {
        let w = World::new(false);
        let sp = service(&w);
        sp.poll().await;
        op(&sp, "voice_start", Value::Null).await;
        op(&sp, "voice_end", Value::Null).await;
        tokio::time::sleep(Duration::from_millis(150)).await;
        assert_eq!(w.count("control pause") + w.count("control play"), 0);
    }

    #[tokio::test]
    async fn voice_does_not_resume_after_user_intervened() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await;
        op(&sp, "voice_start", Value::Null).await;
        op(&sp, "pause", Value::Null).await; // человек сам сказал «пауза»
        op(&sp, "voice_end", Value::Null).await;
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert!(!*w.playing.lock().unwrap());
    }

    #[tokio::test]
    async fn overlapping_voice_replies_resume_once_after_the_last() {
        let w = World::new(true);
        let sp = service(&w);
        sp.poll().await;
        op(&sp, "voice_start", Value::Null).await;
        op(&sp, "voice_start", Value::Null).await;
        op(&sp, "voice_end", Value::Null).await;
        tokio::time::sleep(Duration::from_millis(120)).await;
        assert!(!*w.playing.lock().unwrap());
        op(&sp, "voice_end", Value::Null).await;
        tokio::time::sleep(Duration::from_millis(200)).await;
        assert!(*w.playing.lock().unwrap());
        assert_eq!(w.count("control play"), 1);
    }

    #[tokio::test]
    async fn tick_emits_state_and_play_events_on_change_only() {
        let w = World::new(true);
        let events: Arc<Mutex<Vec<String>>> = Arc::default();
        let log = events.clone();
        let sp = Spotify::new(
            w.clone(),
            Arc::new(move |m, _| {
                log.lock().unwrap().push(m);
                Box::pin(async { Ok(json!({})) })
            }),
        );
        sp.tick().await;
        sp.tick().await;
        let got = events.lock().unwrap().clone();
        assert_eq!(got.iter().filter(|e| *e == "music.state").count(), 1, "{got:?}");
        assert_eq!(got.iter().filter(|e| *e == "music.play").count(), 1);
    }

    struct Missing;
    impl Runner for Missing {
        fn installed(&self) -> bool {
            false
        }
        fn run(&self, _: Cmd) -> RunFuture<'_> {
            Box::pin(async { Err("нет".into()) })
        }
    }
    #[tokio::test]
    async fn reports_missing_soloist() {
        let sp = Spotify::new(Arc::new(Missing), Arc::new(|_, _| Box::pin(async { Ok(json!({})) })));
        let r = op(&sp, "pause", Value::Null).await;
        assert!(r["error"].as_str().unwrap().contains("Soloist не найден"));
    }
}
