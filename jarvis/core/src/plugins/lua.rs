//! Lua 5.4 внутри процесса. У каждого плагина своя VM и ограниченная очередь вызовов.
use super::{Host, Manifest, valid_tool_name};
use mlua::{Function, HookTriggers, Lua, LuaOptions, LuaSerdeExt, StdLib, Table, VmState};
use serde_json::{Map, Value, json};
use std::{
    collections::BTreeMap,
    path::PathBuf,
    sync::{
        Arc, Mutex, Weak,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};
use tokio::sync::{mpsc, oneshot};
#[path = "lua_api.rs"]
mod api;

type Reply = oneshot::Sender<Result<Value, String>>;
enum Request {
    Call(String, Value, Reply),
    Configure(Map<String, Value>),
    Stop,
}
pub struct Script {
    tx: mpsc::Sender<Request>,
    tools: Vec<Value>,
    commands: bool,
    chat: bool,
    stt: bool,
    tts: bool,
    tts_voices: bool,
    service: bool,
    status: Arc<Mutex<String>>,
    stopped: Arc<AtomicBool>,
    /// Закрывается, когда поток плагина завершился: после stop() таймеры уже ничего не пишут.
    exited: Mutex<Option<oneshot::Receiver<()>>>,
}

struct Capabilities {
    tools: Vec<Value>,
    commands: bool,
    chat: bool,
    stt: bool,
    tts: bool,
    tts_voices: bool,
    service: bool,
}

struct Vm {
    lua: Lua,
    exports: Table,
    handlers: BTreeMap<String, Function>,
    deadline: Arc<Mutex<Instant>>,
    wall: Arc<Mutex<Instant>>,
    timers: api::Timers,
}

impl Vm {
    fn open(
        host: Weak<Host>,
        m: &Manifest,
        dir: PathBuf,
        settings: Map<String, Value>,
        status: Arc<Mutex<String>>,
        stopped: Arc<AtomicBool>,
        runtime: tokio::runtime::Handle,
    ) -> mlua::Result<(Self, Capabilities)> {
        let root = dir.canonicalize().map_err(mlua::Error::external)?;
        let main = dir
            .join(&m.main)
            .canonicalize()
            .map_err(mlua::Error::external)?;
        if !main.starts_with(&root) {
            return Err(mlua::Error::external("main вне папки плагина"));
        }
        let source = std::fs::read(&main).map_err(mlua::Error::external)?;
        if source.len() > 512 * 1024 {
            return Err(mlua::Error::external("скрипт больше 512 КБ"));
        }
        let lua = Lua::new_with(
            StdLib::TABLE | StdLib::STRING | StdLib::MATH | StdLib::UTF8,
            LuaOptions::default(),
        )?;
        lua.set_memory_limit(12 * 1024 * 1024)?;
        // Нет загрузки файлов/байткода, package, os, io, debug и корутин, обходящих hook.
        for name in ["dofile", "loadfile", "load", "collectgarbage"] {
            lua.globals().set(name, mlua::Value::Nil)?;
        }
        let deadline = Arc::new(Mutex::new(Instant::now() + Duration::from_secs(2)));
        let until = deadline.clone();
        let wall = Arc::new(Mutex::new(Instant::now() + Duration::from_secs(20)));
        let wall_hook = wall.clone();
        let stop_hook = stopped.clone();
        lua.set_hook(
            HookTriggers::new().every_nth_instruction(1000),
            move |_, _| {
                if stop_hook.load(Ordering::Relaxed)
                    || Instant::now() > *until.lock().unwrap()
                    || Instant::now() > *wall_hook.lock().unwrap()
                {
                    return Err(mlua::Error::external(
                        "Lua: вызов прерван или превышено время выполнения",
                    ));
                }
                Ok(VmState::Continue)
            },
        )?;
        let api = lua.create_table()?;
        let timers = api::install(
            &lua,
            &api,
            &m,
            host.clone(),
            stopped.clone(),
            runtime,
            deadline.clone(),
            wall.clone(),
        )?;
        api.set("settings", lua.to_value(&settings)?)?;
        api.set(
            "info",
            lua.to_value(
                &host
                    .upgrade()
                    .map(|h| (h.hooks.info)())
                    .unwrap_or(json!({})),
            )?,
        )?;
        let id = m.id.clone();
        api.set(
            "log",
            lua.create_function(move |_, text: String| {
                log::info!("плагин {id}: {}", super::clip(&text, 600));
                Ok(())
            })?,
        )?;
        api.set(
            "status",
            lua.create_function(move |_, text: String| {
                *status.lock().unwrap() = super::clip(&text, 200);
                Ok(())
            })?,
        )?;
        if m.permissions.iter().any(|p| p == "say") {
            let host = host.clone();
            let stopped = stopped.clone();
            api.set(
                "say",
                lua.create_function(move |_, text: String| {
                    if stopped.load(Ordering::Relaxed) {
                        return Err(mlua::Error::external("плагин остановлен"));
                    }
                    if let Some(h) = host.upgrade() {
                        (h.hooks.say)(super::clip(&text, 600));
                    }
                    Ok(())
                })?,
            )?;
        }
        let json_api = lua.create_table()?;
        json_api.set(
            "encode",
            lua.create_function(|lua, v: mlua::Value| {
                let v: Value = lua.from_value(v)?;
                serde_json::to_string(&v).map_err(mlua::Error::external)
            })?,
        )?;
        json_api.set(
            "decode",
            lua.create_function(|lua, s: String| {
                let v: Value = serde_json::from_str(&s).map_err(mlua::Error::external)?;
                lua.to_value(&v)
            })?,
        )?;
        api.set("json", json_api)?;
        lua.globals().set("jarvis", api)?;
        let exports: Table = lua
            .load(&source)
            .set_name(&m.main)
            .set_mode(mlua::ChunkMode::Text)
            .eval()?;
        let mut defs = vec![];
        let mut handlers = BTreeMap::new();
        if let Some(ts) = exports.get::<Option<Table>>("tools")? {
            if !m.permissions.iter().any(|p| p == "tools") {
                return Err(mlua::Error::external("нужно право tools"));
            }
            for t in ts.sequence_values::<Table>() {
                let t = t?;
                let name: String = t.get("name")?;
                if !valid_tool_name(&name) || handlers.contains_key(&name) {
                    return Err(mlua::Error::external(
                        "недопустимое или повторное имя инструмента",
                    ));
                }
                let description: String = t.get("description")?;
                let parameters: Value = lua.from_value(t.get("parameters")?)?;
                if !parameters.is_object() {
                    return Err(mlua::Error::external(
                        "parameters должен быть объектом JSON Schema",
                    ));
                }
                handlers.insert(name.clone(), t.get::<Function>("handler")?);
                defs.push(json!({"type":"function","name":name,"description":description,"parameters":parameters}));
                if defs.len() > 64 {
                    return Err(mlua::Error::external("слишком много инструментов"));
                }
            }
        }
        let commands = exports.get::<Option<Function>>("command")?.is_some();
        if commands && !m.permissions.iter().any(|p| p == "commands") {
            return Err(mlua::Error::external("нужно право commands"));
        }
        let chat = exports.get::<Option<Function>>("chat")?.is_some();
        if chat != m.provides("llm") {
            return Err(mlua::Error::external("Для текстового провайдера нужны provides: [\"llm\"] и функция chat"));
        }
        let stt = exports.get::<Option<Function>>("transcribe")?.is_some();
        if stt != m.provides("stt") {
            return Err(mlua::Error::external("Для провайдера распознавания нужны provides: [\"stt\"] и функция transcribe"));
        }
        let tts = exports.get::<Option<Function>>("speak")?.is_some();
        if tts != m.provides("tts") {
            return Err(mlua::Error::external("Для провайдера озвучки нужны provides: [\"tts\"] и функция speak"));
        }
        let tts_voices = tts && exports.get::<Option<Function>>("voices")?.is_some();
        let service = exports.get::<Option<Function>>("service")?.is_some();
        if service
            && !(super::modules::official_lua_service(&m.id)
                && m.provides == [m.id.clone()]
                && host.upgrade().is_some_and(|h| h.official(&m.id)))
        {
            return Err(mlua::Error::external("Недостаточно прав для сервисного провайдера"));
        }
        let vm = Self {
            lua,
            exports,
            handlers,
            deadline,
            wall,
            timers,
        };
        vm.lifecycle("init")?;
        Ok((
            vm,
            Capabilities {
                tools: defs,
                commands,
                chat,
                stt,
                tts,
                tts_voices,
                service,
            },
        ))
    }
    fn budget_for(&self, wall_secs: u64) {
        *self.deadline.lock().unwrap() = Instant::now() + Duration::from_secs(2);
        *self.wall.lock().unwrap() = Instant::now() + Duration::from_secs(wall_secs);
    }
    fn budget(&self) {
        self.budget_for(20);
    }
    fn tick(&self) {
        for (id, function) in api::due(&self.timers) {
            self.budget();
            if let Err(e) = function.call::<()>(()) {
                self.timers.lock().unwrap().remove(&id);
                log::info!("таймер Lua: {}", super::clip(&e.to_string(), 200));
            }
        }
    }
    fn lifecycle(&self, name: &str) -> mlua::Result<()> {
        self.budget();
        if let Some(f) = self.exports.get::<Option<Function>>(name)? {
            f.call::<()>(())?;
        }
        Ok(())
    }
    fn call(&self, name: &str, args: Value) -> mlua::Result<Value> {
        let f = match name {
            "stt.transcribe" => {
                self.budget_for(30);
                self.exports.get::<Function>("transcribe")?
            }
            "tts.speak" => {
                self.budget_for(30);
                self.exports.get::<Function>("speak")?
            }
            "tts.voices" => {
                self.budget_for(20);
                self.exports.get::<Function>("voices")?
            }
            "command" => {
                self.budget();
                self.exports.get::<Function>("command")?
            }
            "llm.chat" => {
                self.budget();
                self.exports.get::<Function>("chat")?
            }
            "service" => {
                self.budget_for(25);
                self.exports.get::<Function>("service")?
            }
            _ => {
                self.budget();
                self.handlers
                    .get(name)
                    .ok_or_else(|| mlua::Error::external("нет инструмента"))?
                    .clone()
            }
        };
        let result: mlua::Value = f.call(self.lua.to_value(&args)?)?;
        self.lua.from_value(result)
    }
    fn configure(&self, settings: Map<String, Value>) -> mlua::Result<()> {
        self.budget();
        let api: Table = self.lua.globals().get("jarvis")?;
        let value = self.lua.to_value(&settings)?;
        api.set("settings", value.clone())?;
        if let Some(f) = self.exports.get::<Option<Function>>("configure")? {
            f.call::<()>(value)?;
        }
        Ok(())
    }
}

impl Script {
    pub async fn start(
        host: Arc<Host>,
        m: Manifest,
        dir: PathBuf,
        settings: Map<String, Value>,
    ) -> Result<Arc<Self>, String> {
        let (tx, mut rx) = mpsc::channel(16);
        let (ready, wait) = oneshot::channel();
        let (exit_tx, exited) = oneshot::channel::<()>();
        let status = Arc::new(Mutex::new(String::new()));
        let stopped = Arc::new(AtomicBool::new(false));
        let (st, stop) = (status.clone(), stopped.clone());
        let host = Arc::downgrade(&host);
        let runtime = tokio::runtime::Handle::current();
        std::thread::Builder::new()
            .name(format!("lua-{}", m.id))
            .stack_size(512 * 1024)
            .spawn(move || {
                let _exit = exit_tx; // уходит последним, после vm: тогда stop() знает, что поток закончил
                let (vm, caps) = match Vm::open(
                    host,
                    &m,
                    dir,
                    settings,
                    st.clone(),
                    stop.clone(),
                    runtime.clone(),
                ) {
                    Ok(v) => v,
                    Err(e) => {
                        let _ = ready.send(Err(e.to_string()));
                        return;
                    }
                };
                if ready.send(Ok(caps)).is_err() {
                    return;
                }
                while !stop.load(Ordering::Relaxed) {
                    vm.tick();
                    let next = api::next_at(&vm.timers);
                    let req = match runtime.block_on(async {
                        match next {
                            Some(at) => {
                                let wait = at.saturating_duration_since(Instant::now());
                                tokio::time::timeout(wait, rx.recv()).await.ok()
                            }
                            None => Some(rx.recv().await),
                        }
                    }) {
                        Some(Some(req)) => req,
                        Some(None) => break,
                        None => continue,
                    };
                    match req {
                        Request::Stop => {
                            break;
                        }
                        Request::Configure(s) => {
                            if let Err(e) = vm.configure(s) {
                                *st.lock().unwrap() = super::clip(&e.to_string(), 200);
                            }
                        }
                        Request::Call(name, args, reply) => {
                            if !reply.is_closed() {
                                let _ = reply.send(vm.call(&name, args).map_err(|e| e.to_string()));
                            }
                        }
                    }
                }
                stop.store(true, Ordering::Relaxed);
                drop(vm); // таймеры и хранилище закрываются до сигнала о выходе
            })
            .map_err(|e| e.to_string())?;
        let caps = wait.await.map_err(|e| e.to_string())??;
        Ok(Arc::new(Self {
            tx,
            tools: caps.tools,
            commands: caps.commands,
            chat: caps.chat,
            stt: caps.stt,
            tts: caps.tts,
            tts_voices: caps.tts_voices,
            service: caps.service,
            status,
            stopped,
            exited: Mutex::new(Some(exited)),
        }))
    }
    pub fn running(&self) -> bool {
        !self.stopped.load(Ordering::Relaxed)
    }
    pub fn supports_service(&self) -> bool {
        self.service && self.running()
    }
    pub async fn service(&self, method: &str, params: Value, timeout: Duration) -> Result<Value, String> {
        if !self.supports_service() {
            return Err("Плагин отключён".into());
        }
        self.request_timeout("service", json!({"method": method, "params": params}), timeout).await
    }
    pub fn tools(&self) -> Vec<Value> {
        if self.stopped.load(Ordering::Relaxed) {
            vec![]
        } else {
            self.tools.clone()
        }
    }
    async fn request(&self, name: &str, args: Value) -> Result<Value, String> {
        self.request_timeout(name, args, Duration::from_secs(21)).await
    }
    async fn request_timeout(&self, name: &str, args: Value, timeout: Duration) -> Result<Value, String> {
        if self.stopped.load(Ordering::Relaxed) {
            return Err("плагин остановлен".into());
        }
        let (tx, rx) = oneshot::channel();
        self.tx
            .try_send(Request::Call(name.into(), args, tx))
            .map_err(|e| e.to_string())?;
        tokio::time::timeout(timeout, rx)
            .await
            .map_err(|_| "Lua: время ожидания истекло".to_string())?
            .map_err(|e| e.to_string())?
    }

    pub fn supports_chat(&self) -> bool {
        self.chat && !self.stopped.load(Ordering::Relaxed)
    }

    pub fn supports_stt(&self) -> bool {
        self.stt && !self.stopped.load(Ordering::Relaxed)
    }

    pub fn supports_tts(&self) -> bool {
        self.tts && !self.stopped.load(Ordering::Relaxed)
    }

    pub fn supports_tts_voices(&self) -> bool {
        self.tts_voices && !self.stopped.load(Ordering::Relaxed)
    }

    pub async fn chat(&self, args: Value) -> Result<Value, String> {
        if !self.supports_chat() {
            return Err("Текстовый плагин отключён".into());
        }
        self.request("llm.chat", args).await
    }

    pub async fn transcribe(&self, args: Value) -> Result<Value, String> {
        if !self.supports_stt() {
            return Err("Плагин распознавания отключён".into());
        }
        self.request_timeout("stt.transcribe", args, Duration::from_secs(32)).await
    }

    pub async fn speak(&self, args: Value) -> Result<Value, String> {
        if !self.supports_tts() {
            return Err("Плагин озвучки отключён".into());
        }
        self.request_timeout("tts.speak", args, Duration::from_secs(32)).await
    }

    pub async fn voices(&self) -> Result<Value, String> {
        if !self.supports_tts() {
            return Err("Плагин озвучки отключён".into());
        }
        if !self.supports_tts_voices() {
            return Ok(json!([]));
        }
        self.request("tts.voices", Value::Null).await
    }
    pub async fn call_tool(&self, name: &str, args: &str) -> Option<String> {
        if !self.tools.iter().any(|t| t["name"] == name) {
            return None;
        }
        let args = match serde_json::from_str(args) {
            Ok(v) => v,
            Err(e) => return Some(format!("Неверные аргументы: {e}")),
        };
        Some(match self.request(name, args).await {
            Ok(Value::String(s)) => s,
            Ok(v) => v.to_string(),
            Err(e) => format!("Ошибка Lua-плагина: {e}"),
        })
    }
    pub async fn command(&self, text: &str) -> Option<String> {
        if !self.commands {
            return None;
        }
        match self
            .request("command", Value::String(super::command_text(text)))
            .await
        {
            Ok(Value::String(s)) => Some(s),
            Ok(v) if v["handled"].as_bool() == Some(true) => {
                Some(v["answer"].as_str().unwrap_or("").into())
            }
            Err(e) => {
                log::info!("команда Lua: {e}");
                None
            }
            _ => None,
        }
    }
    pub fn status(&self) -> String {
        self.status.lock().unwrap().clone()
    }
    pub fn configure(&self, s: Map<String, Value>) {
        if let Err(e) = self.tx.try_send(Request::Configure(s)) {
            *self.status.lock().unwrap() = format!("настройки не применены: {e}");
        }
    }
    pub async fn stop(&self) {
        self.stopped.store(true, Ordering::Relaxed);
        let _ = self.tx.try_send(Request::Stop);
        // дождаться, пока поток доработает текущий вызов или таймер (их время и так ограничено)
        let exited = self.exited.lock().unwrap().take();
        if let Some(rx) = exited {
            let _ = tokio::time::timeout(Duration::from_secs(2), rx).await;
        }
    }
}

impl Drop for Vm {
    fn drop(&mut self) {
        self.timers.lock().unwrap().clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::plugins::{Hooks, Host};
    #[tokio::test]
    async fn installs_example_zip_and_runs_it() {
        use std::io::Write;
        let dir =
            std::env::temp_dir().join(format!("jarvis-lua-zip-{}", crate::settings::rand_hex(6)));
        std::fs::create_dir_all(&dir).unwrap();
        let mut zip = zip::ZipWriter::new(std::io::Cursor::new(Vec::new()));
        for (name, source) in [
            (
                "plugin.json",
                include_str!("../../../../plugins/examples/lua-coin/plugin.json"),
            ),
            (
                "main.lua",
                include_str!("../../../../plugins/examples/lua-coin/main.lua"),
            ),
        ] {
            zip.start_file(name, zip::write::SimpleFileOptions::default())
                .unwrap();
            zip.write_all(source.as_bytes()).unwrap();
        }
        let raw = zip.finish().unwrap().into_inner();
        let host = Host::new(dir.clone(), Hooks::default());
        host.install(&raw, "file", false).await.unwrap();
        let answer = host.command("Джарвис, подбрось монетку!").await.unwrap();
        assert!(
            answer == "Выпало: орёл" || answer == "Выпало: решка",
            "{answer}"
        );
        assert!(host.call_tool("flip_coin", "{}").await.is_some());
        host.uninstall("lua-coin").await.unwrap();
        assert!(host.tools().is_empty());
        std::fs::remove_dir_all(dir).unwrap();
    }
    async fn fixture(source: &str, permissions: Vec<&str>) -> (Arc<Host>, PathBuf) {
        fixture_hooks(source, permissions, Hooks::default()).await
    }
    async fn fixture_hooks(
        source: &str,
        permissions: Vec<&str>,
        hooks: Hooks,
    ) -> (Arc<Host>, PathBuf) {
        let dir = std::env::temp_dir().join(format!("jarvis-lua-{}", crate::settings::rand_hex(6)));
        let plugin = dir.join("plugins/demo");
        std::fs::create_dir_all(&plugin).unwrap();
        std::fs::write(plugin.join("main.lua"), source).unwrap();
        std::fs::write(plugin.join("plugin.json"),json!({"id":"demo","name":"Lua demo","version":"1","kind":"lua","category":"fun","main":"main.lua","permissions":permissions,
            "network_allow":["127.0.0.1"],"settings":[{"key":"answer","type":"text","default":"орел"}]}).to_string()).unwrap();
        let host = Host::new(dir.clone(), hooks);
        host.load().await;
        (host, dir)
    }
    #[tokio::test]
    async fn lifecycle_tools_commands_and_settings() {
        let (host,dir)=fixture(r#"
            assert(os == nil and io == nil and package == nil and debug == nil and loadfile == nil and load == nil)
            return {
              init = function() jarvis.status('готов') end,
              tools = {{name='coin',description='монетка',parameters={type='object'},handler=function(args) return jarvis.settings.answer .. args.tail end}},
              command = function(text) if text == 'монетка' then return 'орел' end end,
              configure = function(s) jarvis.status(s.answer) end
            }
        "#,vec!["tools","commands"]).await;
        assert_eq!(host.list()[0]["running"], true, "{:?}", host.list());
        assert_eq!(host.tools()[0]["name"], "coin");
        assert_eq!(
            host.call_tool("coin", r#"{"tail":"!"}"#).await.as_deref(),
            Some("орел!")
        );
        assert_eq!(host.command("монетка").await.as_deref(), Some("орел"));
        assert_eq!(host.command("другое").await, None);
        host.set_settings(
            "demo",
            &json!({"answer":"решка"}).as_object().unwrap().clone(),
        )
        .unwrap();
        assert_eq!(
            host.call_tool("coin", r#"{"tail":"!"}"#).await.as_deref(),
            Some("решка!")
        );
        host.set_enabled("demo", false).await.unwrap();
        assert!(host.tools().is_empty());
        host.set_enabled("demo", true).await.unwrap();
        assert_eq!(
            host.call_tool("coin", r#"{"tail":"!"}"#).await.as_deref(),
            Some("решка!")
        );
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }
    #[tokio::test]
    async fn rejects_missing_permissions_and_core_tool_names() {
        for (src, perms) in [
            ("return {command=function() return 'yes' end}", vec![]),
            (
                "return {tools={{name='smart_home',description='x',parameters={},handler=function() end}}}",
                vec!["tools"],
            ),
            ("jarvis.say('x');return {}", vec![]),
        ] {
            let (host, dir) = fixture(src, perms).await;
            assert_eq!(host.list()[0]["running"], false);
            assert!(!host.list()[0]["error"].as_str().unwrap().is_empty());
            std::fs::remove_dir_all(dir).unwrap();
        }
    }
    #[tokio::test]
    async fn limits_cpu_and_memory_and_recovers_after_error() {
        let (host,dir)=fixture(r#"return {tools={
            {name='spin',description='x',parameters={},handler=function() while true do end end},
            {name='alloc',description='x',parameters={},handler=function() return string.rep('x',32*1024*1024) end},
            {name='ok',description='x',parameters={},handler=function() return 'alive' end}
        }}"#,vec!["tools"]).await;
        assert!(
            host.call_tool("spin", "{}")
                .await
                .unwrap()
                .contains("превышено")
        );
        assert!(
            host.call_tool("alloc", "{}")
                .await
                .unwrap()
                .contains("memory")
        );
        assert_eq!(host.call_tool("ok", "{}").await.as_deref(), Some("alive"));
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }
    #[tokio::test]
    async fn storage_persists_and_timer_callbacks_stop_with_plugin() {
        let source = r#"
            local n = jarvis.store.get('n') or 0
            return {init=function()
                jarvis.store.set('n',n+1)
                local id=jarvis.timers.every(100,function() jarvis.store.set('tick',(jarvis.store.get('tick') or 0)+1) end)
                jarvis.timers.after(10,function() jarvis.status('timer fired') end)
                jarvis.store.set('cancelled',jarvis.timers.cancel(jarvis.timers.after(5000,function() error('must not run') end)))
            end,tools={{name='stored',description='x',parameters={},handler=function() return jarvis.store.get('n') end}}}
        "#;
        let (host, dir) = fixture(source, vec!["tools", "storage", "timers"]).await;
        assert_eq!(host.list()[0]["running"], true, "{:?}", host.list());
        assert_eq!(host.call_tool("stored", "{}").await.as_deref(), Some("1"));
        tokio::time::timeout(Duration::from_secs(2), async {
            loop {
                if host.list()[0]["status"] == "timer fired" {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .unwrap();
        host.set_enabled("demo", false).await.unwrap();
        tokio::time::sleep(Duration::from_millis(150)).await;
        let file = dir.join("plugins-data/demo.json");
        let before = std::fs::read(&file).unwrap();
        tokio::time::sleep(Duration::from_millis(150)).await;
        assert_eq!(std::fs::read(&file).unwrap(), before);
        host.set_enabled("demo", true).await.unwrap();
        assert_eq!(host.call_tool("stored", "{}").await.as_deref(), Some("2"));
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }
    #[tokio::test]
    async fn http_checks_redirect_permissions_and_honors_blocked_vpn() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = tokio::spawn(async move {
            for redirect in [false, true] {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut request = [0; 4096];
                socket.read(&mut request).await.unwrap();
                let bytes = if redirect {
                    "HTTP/1.1 302 Found\r\nLocation: http://forbidden.invalid/\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                } else {
                    "HTTP/1.1 200 OK\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"value\":7}"
                };
                socket.write_all(bytes.as_bytes()).await.unwrap();
            }
        });
        let source = format!(
            r#"return {{tools={{{{name='http',description='x',parameters={{}},handler=function(a)
          if a.denied then return jarvis.fetch('http://forbidden.invalid/') end
          return jarvis.fetch('http://127.0.0.1:{port}/')
        end}}}}}}"#
        );
        let (host, dir) = fixture(&source, vec!["tools", "network"]).await;
        assert!(
            host.call_tool("http", r#"{"denied":true}"#)
                .await
                .unwrap()
                .contains("network_allow")
        );
        let response: Value =
            serde_json::from_str(&host.call_tool("http", "{}").await.unwrap()).unwrap();
        assert_eq!(response["status"], 200);
        assert_eq!(response["body"], r#"{"value":7}"#);
        assert!(
            host.call_tool("http", "{}")
                .await
                .unwrap()
                .contains("запрещённый")
        );
        server.await.unwrap();
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
        let hooks = Hooks {
            network: Box::new(|| json!({"error":"VPN blocked"})),
            ..Default::default()
        };
        let (host, dir) = fixture_hooks(&source, vec!["tools", "network"], hooks).await;
        assert!(
            host.call_tool("http", "{}")
                .await
                .unwrap()
                .contains("VPN blocked")
        );
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }
    #[tokio::test]
    async fn bundled_lua_plugins_preserve_commands_and_tool_results() {
        let weather = concat!(
            r#"
          jarvis.fetch=function(url)
            local value
            if string.find(url,'geocoding',1,true) then value={results={{name='Минск',latitude=53.9,longitude=27.5}}}
            else value={current={temperature_2m=4,apparent_temperature=2,weather_code=3,wind_speed_10m=5},daily={weather_code={3,61,0},temperature_2m_max={6,8,10},temperature_2m_min={1,2,3},precipitation_probability_max={10,70,0}}} end
            return {ok=true,status=200,body=jarvis.json.encode(value)}
          end
        "#,
            include_str!("../../../../plugins/weather/main.lua")
        );
        let (host, dir) = fixture(weather, vec!["tools", "commands", "storage"]).await;
        let answer = host
            .call_tool("get_weather", r#"{"city":"Минск","day":"tomorrow"}"#)
            .await
            .unwrap();
        assert!(
            answer.contains("Минск завтра") && answer.contains("70%"),
            "{answer}"
        );
        assert_eq!(host.command("переведи слово погода").await, None);
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
        let currency = concat!(
            r#"jarvis.fetch=function(url) return {ok=true,status=200,body=jarvis.json.encode({Valute={USD={Value=90,Nominal=1},EUR={Value=100,Nominal=1}}})} end
        "#,
            include_str!("../../../../plugins/currency/main.lua")
        );
        let (host, dir) = fixture(currency, vec!["tools", "commands", "storage"]).await;
        let answer = host.command("сколько 100 евро").await.unwrap();
        assert!(
            answer.contains("100 EUR") && answer.contains("10000"),
            "{answer}"
        );
        assert!(
            host.call_tool("exchange_rate", r#"{"currency":"USD"}"#)
                .await
                .unwrap()
                .contains("90")
        );
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
        let (host, dir) = fixture(
            include_str!("../../../../plugins/dice/main.lua"),
            vec!["tools", "commands"],
        )
        .await;
        assert_eq!(
            host.call_tool("random_number", r#"{"min":2,"max":2}"#)
                .await
                .as_deref(),
            Some("2")
        );
        assert!(
            host.command("брось кубик")
                .await
                .unwrap()
                .starts_with("Выпало ")
        );
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn fetch_supports_multipart_and_base64_response() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut buf = Vec::new();
            let mut tmp = [0u8; 4096];
            loop {
                let n = socket.read(&mut tmp).await.unwrap();
                if n == 0 { break; }
                buf.extend_from_slice(&tmp[..n]);
                if let Some(hdr_end) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
                    let hdr = String::from_utf8_lossy(&buf[..hdr_end]).to_ascii_lowercase();
                    let cl = hdr.lines().find_map(|l| l.strip_prefix("content-length:"))
                        .and_then(|v| v.trim().parse::<usize>().ok()).unwrap_or(0);
                    if buf.len() >= hdr_end + 4 + cl {
                        break;
                    }
                }
            }
            let text = String::from_utf8_lossy(&buf);
            assert!(text.contains("multipart/form-data; boundary=jarvis-"));
            assert!(text.contains("name=\"model\""));
            assert!(text.contains("whisper-1"));
            assert!(text.contains("name=\"file\"; filename=\"a.wav\""));
            assert!(text.contains("Content-Type: audio/wav"));
            assert!(text.contains("RIFF"));
            let reply = b"\x01\x02\x03\x04";
            let resp = format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", reply.len());
            socket.write_all(resp.as_bytes()).await.unwrap();
            socket.write_all(reply).await.unwrap();
        });
        let source = format!(
            r#"return {{tools={{{{name='bin',description='x',parameters={{}},handler=function()
                return jarvis.fetch('http://127.0.0.1:{port}/upload', {{
                    method = 'POST',
                    multipart = {{
                        {{ name = 'model', value = 'whisper-1' }},
                        {{ name = 'file', filename = 'a.wav', content_type = 'audio/wav', data_base64 = 'UklGRg==' }}
                    }},
                    response_encoding = 'base64'
                }})
            end}}}}}}"#
        );
        let (host, dir) = fixture(&source, vec!["tools", "network"]).await;
        let out: Value = serde_json::from_str(&host.call_tool("bin", "{}").await.unwrap()).unwrap();
        assert_eq!(out["status"], 200);
        assert_eq!(out["ok"], true);
        assert_eq!(out["body_base64"], "AQIDBA==");
        server.await.unwrap();
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn stt_and_tts_lua_plugins_work_and_validate_provides() {
        let dir = std::env::temp_dir().join(format!("jarvis-lua-st-{}", crate::settings::rand_hex(6)));
        let bad = dir.join("plugins/bad-stt");
        std::fs::create_dir_all(&bad).unwrap();
        std::fs::write(bad.join("plugin.json"), json!({
            "id":"bad-stt","name":"Bad","version":"1","kind":"lua","category":"assistant","main":"main.lua","provides":["stt"]
        }).to_string()).unwrap();
        std::fs::write(bad.join("main.lua"), "return {}").unwrap();

        let good = dir.join("plugins/custom-av");
        std::fs::create_dir_all(&good).unwrap();
        std::fs::write(good.join("plugin.json"), json!({
            "id":"custom-av","name":"Custom AV","version":"1","kind":"lua","category":"assistant","main":"main.lua","provides":["stt","tts"]
        }).to_string()).unwrap();
        std::fs::write(good.join("main.lua"), r#"return {
            transcribe = function(req) return { text = 'привет ' .. (req.model or '') } end,
            speak = function(req) return { audio = req.voice .. ':' .. req.text, rate = 16000 } end,
            voices = function() return { { id = 'v1', name = 'Voice 1', note = 'test' } } end
        }"#).unwrap();

        let host = Host::new(dir.clone(), Hooks::default());
        host.load().await;
        let list = host.list();
        assert_eq!(list.iter().find(|p| p["id"] == "bad-stt").unwrap()["running"], false);
        assert_eq!(host.stt_plugins(), vec![json!({"id": "custom-av", "name": "Custom AV"})]);
        assert_eq!(host.tts_plugins(), vec![json!({"id": "custom-av", "name": "Custom AV"})]);
        let stt = host.plugin_stt("custom-av", json!({"model": "m1"})).await.unwrap();
        assert_eq!(stt["text"], "привет m1");
        let tts = host.plugin_tts("custom-av", json!({"voice": "v1", "text": "ок"})).await.unwrap();
        assert_eq!(tts["audio"], "v1:ок");
        assert_eq!(tts["rate"], 16000);
        let voices = host.plugin_tts_voices("custom-av").await.unwrap();
        assert_eq!(voices[0]["id"], "v1");
        host.stop_all().await;
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn yandexhome_lua_matches_rust_behavior() {
        use axum::{
            Json, Router,
            body::Bytes,
            extract::{Path, State as AxumState},
            http::{HeaderMap, StatusCode},
            response::{IntoResponse, Response},
            routing::{get, post},
        };
        use std::sync::atomic::{AtomicU8, AtomicUsize};

        #[derive(Clone)]
        struct Fake {
            catalog: Arc<Mutex<Value>>,
            posts: Arc<Mutex<Vec<(String, Value)>>>,
            gets: Arc<AtomicUsize>,
            mode: Arc<AtomicU8>,
        }
        fn authorized(h: &HeaderMap) -> bool {
            h.get("authorization").is_some_and(|s| s == "Bearer test-token")
        }
        async fn info(AxumState(s): AxumState<Fake>, h: HeaderMap) -> Response {
            s.gets.fetch_add(1, Ordering::Relaxed);
            if !authorized(&h) {
                return StatusCode::UNAUTHORIZED.into_response();
            }
            Json(s.catalog.lock().unwrap().clone()).into_response()
        }
        fn ack(id: &str, actions: &[Value]) -> Value {
            json!({"id": id, "capabilities": actions.iter().map(|a| json!({
                "type": a["type"], "state": {"instance": a["state"]["instance"], "action_result": {"status": "DONE"}}
            })).collect::<Vec<_>>()})
        }
        async fn action(AxumState(s): AxumState<Fake>, Path(path): Path<String>, h: HeaderMap, body: Bytes) -> Response {
            if !authorized(&h) {
                return StatusCode::UNAUTHORIZED.into_response();
            }
            let body: Value = if body.is_empty() { Value::Null } else { serde_json::from_slice(&body).unwrap() };
            s.posts.lock().unwrap().push((path.clone(), body.clone()));
            let mode = s.mode.load(Ordering::Relaxed);
            if mode == 7 { return StatusCode::INTERNAL_SERVER_ERROR.into_response(); }
            if mode == 8 { return "not-json".into_response(); }
            if path.starts_with("scenarios/") {
                return Json(json!({"status": if mode == 0 { "ok" } else { "error" }})).into_response();
            }
            let mut devices: Vec<Value> = if path == "devices/actions" {
                body["devices"].as_array().unwrap().iter()
                    .map(|d| ack(d["id"].as_str().unwrap(), d["actions"].as_array().unwrap()))
                    .collect()
            } else {
                let c = s.catalog.lock().unwrap();
                let group = c["groups"].as_array().unwrap().iter()
                    .find(|g| path == format!("groups/{}/actions", g["id"].as_str().unwrap())).unwrap();
                group["devices"].as_array().unwrap().iter()
                    .map(|id| ack(id.as_str().unwrap(), body["actions"].as_array().unwrap()))
                    .collect()
            };
            match mode {
                1 => { devices.pop(); }
                2 => { if let Some(d) = devices.first_mut() { d["capabilities"].as_array_mut().unwrap().remove(0); } }
                3 => { devices[0]["capabilities"][0]["state"]["action_result"]["status"] = "ERROR".into(); }
                4 => { devices[0]["id"] = "unknown-id".into(); }
                5 => { devices.push(devices[0].clone()); }
                _ => {}
            }
            Json(json!({"status": if mode == 6 { "error" } else { "ok" }, "devices": devices})).into_response()
        }

        let catalog_json: Value = serde_json::from_str(include_str!("../../../../jarvis/modules/yandexhome/src/catalog-test.json")).unwrap();
        let fake = Fake {
            catalog: Arc::new(Mutex::new(catalog_json)),
            posts: Arc::default(),
            gets: Arc::default(),
            mode: Arc::default(),
        };
        let app = Router::new()
            .route("/v1.0/user/info", get(info))
            .route("/v1.0/{*path}", post(action))
            .with_state(fake.clone());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/v1.0/", listener.local_addr().unwrap());
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap(); });

        // Reject unofficial upload claiming yandexhome.
        let dir = std::env::temp_dir().join(format!("jarvis-lua-yh-{}", crate::settings::rand_hex(6)));
        std::fs::create_dir_all(&dir).unwrap();
        let host_unoff = Host::new(dir.clone(), Hooks::default());
        let mut zip_bytes = Vec::new();
        {
            use std::io::Write;
            let mut z = zip::ZipWriter::new(std::io::Cursor::new(&mut zip_bytes));
            z.start_file("plugin.json", zip::write::SimpleFileOptions::default()).unwrap();
            z.write_all(include_bytes!("../../../../plugins/yandexhome/plugin.json")).unwrap();
            z.start_file("main.lua", zip::write::SimpleFileOptions::default()).unwrap();
            z.write_all(include_bytes!("../../../../plugins/yandexhome/main.lua")).unwrap();
            z.finish().unwrap();
        }
        assert!(host_unoff.install(&zip_bytes, "file", false).await.is_err());

        // Start official Lua yandexhome with test server URL + configurable network policy.
        let net_route = Arc::new(Mutex::new(json!({})));
        let net_ref = net_route.clone();
        let hooks = Hooks {
            request: Box::new(move |_id, method, _params| {
                let route = net_ref.lock().unwrap().clone();
                Box::pin(async move {
                    if method == "network" { Ok(route) } else { Err("unknown".into()) }
                })
            }),
            ..Default::default()
        };
        let mut manifest: Value = serde_json::from_str(include_str!("../../../../plugins/yandexhome/plugin.json")).unwrap();
        manifest["network_allow"] = json!(["127.0.0.1"]);
        let src = format!(
            "jarvis.yandex_api = '{}'\n{}",
            url,
            include_str!("../../../../plugins/yandexhome/main.lua")
        );
        let pdir = dir.join("plugins/yandexhome");
        std::fs::create_dir_all(&pdir).unwrap();
        std::fs::write(pdir.join("plugin.json"), manifest.to_string()).unwrap();
        std::fs::write(pdir.join("main.lua"), src).unwrap();
        let mut st_map = BTreeMap::new();
        st_map.insert("yandexhome", super::super::PluginState {
            official: true,
            source: crate::DEFAULT_REPO.into(),
            ..Default::default()
        });
        std::fs::write(dir.join("plugins.json"), serde_json::to_vec(&st_map).unwrap()).unwrap();

        let host = Host::new(dir.clone(), hooks);
        host.load().await;
        let rt = host.provider("yandexhome").unwrap();
        let call = |op: &str, args: Value, alice: bool| {
            let rt = rt.clone();
            let body = json!({"op": op, "args": args, "token": "test-token", "room": "Зал", "alice": alice});
            async move {
                rt.request("yandexhome", body, Duration::from_secs(10)).await
            }
        };

        assert_eq!(call("has-catalog", Value::Null, false).await.unwrap(), false);
        assert_eq!(call("fast", "Джарвис, выключи свет!".into(), false).await.unwrap()["answer"], "Готово.");
        assert_eq!(fake.posts.lock().unwrap()[0].0, "devices/actions");
        assert_eq!(fake.posts.lock().unwrap()[0].1["devices"][0]["id"], "l1");

        for (text, want) in [
            ("включи свет на кухне", "l3"),
            ("Выключи стол", "l2"),
            ("включи ленту в спальне", "l4"),
            ("выключи розетку", "s1"),
            ("запусти пылесос", "v1"),
            ("выключи кондей", "ac"),
            ("включи подсветку кровати", "l4"),
        ] {
            let ans = call("fast", text.into(), false).await.unwrap();
            assert_eq!(ans["answer"], "Готово.", "{text}");
            let posts = fake.posts.lock().unwrap();
            assert_eq!(posts.last().unwrap().1["devices"][0]["id"], want, "{text}");
        }

        let ans = call("fast", "Джарвис, выключи весь свет".into(), false).await.unwrap();
        assert_eq!(ans["answer"], "Готово.");
        assert_eq!(fake.posts.lock().unwrap().last().unwrap().1["devices"].as_array().unwrap().len(), 4);

        let ans = call("fast", "Я вернулся".into(), false).await.unwrap();
        assert_eq!(ans["answer"], "Готово.");
        assert_eq!(fake.posts.lock().unwrap().last().unwrap().0, "scenarios/sc1/actions");

        let ans = call("fast", "Джарвис, сделай ленту красной".into(), false).await.unwrap();
        assert_eq!(ans["answer"], "Готово.");
        {
            let posts = fake.posts.lock().unwrap();
            assert_eq!(posts.last().unwrap().1["devices"][0]["actions"][1]["state"]["value"], 0xFF0000);
        }

        let ctx = call("context", Value::Null, false).await.unwrap();
        let ctx_s = ctx.as_str().unwrap();
        assert!(ctx_s.contains("Колонка стоит в комнате «Зал»"), "{ctx_s}");
        assert!(!ctx_s.contains("Станция"), "{ctx_s}");
        assert_eq!(call("has-catalog", Value::Null, false).await.unwrap(), true);
        assert_eq!(call("scenario-match", "Не запускать".into(), false).await.unwrap(), false);
        assert_eq!(call("fast", "Расскажи как включить свет".into(), false).await.unwrap()["handled"], false);

        // Confirmation failures + no POST retry.
        for mode in 1..=8u8 {
            fake.mode.store(mode, Ordering::Relaxed);
            let before = fake.posts.lock().unwrap().len();
            let res = call("control", json!({"device_ids":["l4"],"color":"#00ff00"}).to_string().into(), false).await.unwrap();
            assert_ne!(res, "Готово.", "mode {mode}");
            assert_eq!(fake.posts.lock().unwrap().len(), before + 1);
        }
        for mode in [0u8, 1, 3, 5] {
            fake.mode.store(mode, Ordering::Relaxed);
            let res = call("control", json!({"device_ids":["g1"],"on":true}).to_string().into(), false).await.unwrap();
            assert_eq!(res == "Готово.", mode == 0, "group mode {mode}: {res}");
        }
        fake.mode.store(0, Ordering::Relaxed);

        // Invalid & unknown inputs + partial result + null fields handling.
        for a in [
            json!({"scenario_id":"disabled"}),
            json!({"scenario_id":"../unknown"}),
            json!({"device_ids":["unknown"],"on":true}),
            json!({"device_ids":["ac"],"modes":{"thermostat":"bad"}}),
            json!({"device_ids":["l1"],"color":"invalid"}),
        ] {
            assert_ne!(call("control", a.to_string().into(), false).await.unwrap(), "Готово.");
        }
        let partial = call("control", json!({"device_ids":["l1","unknown"],"on":true}).to_string().into(), false).await.unwrap();
        assert!(partial.as_str().unwrap().contains("Часть действий"));

        // Blocked / invalid VPN route never falls back to direct.
        call("prewarm", Value::Null, false).await.unwrap();
        let gets_before = fake.gets.load(Ordering::Relaxed);
        for bad_route in [
            json!({"error":"VPN недоступен"}),
            json!({"socks":"192.0.2.1:1080","user":"x","pass":"x"}),
            json!({"socks":null}),
            Value::Null,
        ] {
            *net_route.lock().unwrap() = bad_route.clone();
            assert!(call("control", json!({"device_ids":["l1"],"on":true}).to_string().into(), false).await.is_err(), "{bad_route:?}");
        }
        assert_eq!(fake.gets.load(Ordering::Relaxed), gets_before);
        *net_route.lock().unwrap() = json!({"error":"blocked"});
        let fb = call("context", Value::Null, true).await.unwrap();
        assert!(fb.as_str().unwrap().contains("alice_command"));
        *net_route.lock().unwrap() = json!({});

        // Null fields in catalog are tolerated.
        *fake.catalog.lock().unwrap() = json!({"status":"ok","rooms":null,"groups":null,"devices":[{"id":"x","room":null,"capabilities":null}],"scenarios":null});
        call("invalidate", Value::Null, false).await.unwrap();
        assert!(call("context", Value::Null, false).await.unwrap().as_str().unwrap().contains("- x"));

        host.stop_all().await;
        server.abort();
        std::fs::remove_dir_all(dir).unwrap();
    }
}
