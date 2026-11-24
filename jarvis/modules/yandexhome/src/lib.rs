//! Яндекс Умный дом: самостоятельный выбираемый модуль. Сетевой маршрут передаёт хост.
mod control;
mod fast;
mod model;
#[cfg(test)]
mod tests;
use model::{Args, Catalog};
use serde_json::{Value, json};
use std::{
    net::SocketAddr,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, Instant},
};

pub const API: &str = "https://api.iot.yandex.net/v1.0/";
pub struct YandexHome {
    base: reqwest::Url,
    state: Mutex<Option<Arc<State>>>,
    clients: Mutex<Option<(Value, reqwest::Client)>>,
}
struct State {
    token: String,
    cache: Mutex<Option<(Instant, Arc<Catalog>)>>,
    fetch: tokio::sync::Mutex<()>,
    epoch: AtomicU64,
}
impl State {
    fn cached(&self, fresh: bool) -> Option<Arc<Catalog>> {
        self.cache
            .lock()
            .unwrap()
            .as_ref()
            .filter(|(at, _)| !fresh || at.elapsed() < Duration::from_secs(30))
            .map(|(_, c)| c.clone())
    }
    fn invalidate(&self) {
        self.epoch.fetch_add(1, Ordering::SeqCst);
        *self.cache.lock().unwrap() = None;
    }
}
pub(crate) struct Call<'a> {
    home: &'a YandexHome,
    state: Arc<State>,
    http: reqwest::Client,
    room: String,
    alice: bool,
}
impl YandexHome {
    pub fn new() -> Self {
        Self {
            base: reqwest::Url::parse(API).expect("api url"),
            state: Mutex::default(),
            clients: Mutex::default(),
        }
    }
    fn state(&self, token: &str) -> Arc<State> {
        let mut state = self.state.lock().unwrap();
        if state.as_ref().is_none_or(|s| s.token != token) {
            *state = Some(Arc::new(State {
                token: token.into(),
                cache: Mutex::default(),
                fetch: tokio::sync::Mutex::new(()),
                epoch: AtomicU64::new(0),
            }));
        }
        state.as_ref().unwrap().clone()
    }
    fn client(&self, network: &Value) -> Result<reqwest::Client, String> {
        if let Some(error) = network
            .get("error")
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty())
        {
            return Err(error.into());
        }
        if !network.is_object() {
            return Err("Хост не предоставил сетевой маршрут".into());
        }
        let mut clients = self.clients.lock().unwrap();
        if let Some((key, client)) = clients.as_ref() {
            if key == network {
                return Ok(client.clone());
            }
        }
        let mut builder = reqwest::Client::builder()
            .no_proxy()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(10))
            .connect_timeout(Duration::from_secs(5));
        if let Some(socks) = network.get("socks").and_then(Value::as_str) {
            let addr: SocketAddr = socks
                .parse()
                .map_err(|_| "Неверный SOCKS-маршрут".to_string())?;
            let user = network["user"].as_str().unwrap_or("");
            let pass = network["pass"].as_str().unwrap_or("");
            if !addr.ip().is_loopback()
                || addr.port() == 0
                || !(1..=255).contains(&user.len())
                || !(1..=255).contains(&pass.len())
            {
                return Err("Неверный SOCKS-маршрут".into());
            }
            let proxy = reqwest::Proxy::all(format!("socks5h://{addr}"))
                .map_err(|_| "Неверный SOCKS-маршрут".to_string())?
                .basic_auth(user, pass);
            builder = builder.proxy(proxy);
        } else if network.get("socks").is_some()
            || network.get("user").is_some()
            || network.get("pass").is_some()
        {
            return Err("Неполный SOCKS-маршрут".into());
        }
        let client = builder
            .build()
            .map_err(|_| "Не получилось настроить HTTP-клиент".to_string())?;
        *clients = Some((network.clone(), client.clone()));
        Ok(client)
    }
    pub async fn request(&self, p: Value, network: Value) -> Result<Value, String> {
        tokio::time::timeout(Duration::from_secs(25), self.handle(p, network))
            .await
            .map_err(|_| "Яндекс Умный дом не ответил вовремя".to_string())?
    }
    async fn handle(&self, p: Value, network: Value) -> Result<Value, String> {
        let token = p["token"].as_str().unwrap_or("").trim();
        let op = p["op"].as_str().unwrap_or("");
        let text = p["args"].as_str().unwrap_or("");
        let state = self.state(token);
        let room = p["room"].as_str().unwrap_or("").trim();
        let alice = p["alice"].as_bool().unwrap_or(false);
        if op == "has-catalog" {
            return Ok(state
                .cached(false)
                .is_some_and(|c| !c.devices.is_empty() || !c.scenarios.is_empty())
                .into());
        }
        if op == "invalidate" {
            state.invalidate();
            return Ok(true.into());
        }
        let http = match self.client(&network) {
            Ok(c) => c,
            Err(e) => {
                return match op {
                    "context" => Ok(fallback(room, alice).into()),
                    "fast" => Ok(json!({"handled":false,"answer":""})),
                    _ => Err(e),
                };
            }
        };
        let call = Call {
            home: self,
            state,
            http,
            room: room.into(),
            alice,
        };
        match op {
            "settings.validate" => {
                call.catalog(true).await?;
                Ok(true.into())
            }
            "catalog" => {
                let c = call.catalog(p["args"].as_bool().unwrap_or(false)).await?;
                Ok(catalog_view(&c))
            }
            "prewarm" => {
                let _ = tokio::time::timeout(Duration::from_secs(6), call.catalog(false)).await;
                Ok(true.into())
            }
            "context" => Ok(call.context().await.into()),
            "scenario-match" => {
                let c = match call.catalog(false).await {
                    Ok(c) => c,
                    Err(_) => return Ok(false.into()),
                };
                let n = bare(text);
                Ok(c.scenarios
                    .iter()
                    .any(|s| {
                        s.is_active != Some(false)
                            && s.phrases()
                                .into_iter()
                                .chain(std::iter::once(s.name.as_str()))
                                .any(|ph| !norm(ph).is_empty() && norm(ph) == n)
                    })
                    .into())
            }
            "fast" => {
                let result = call.fast(text).await;
                Ok(match result {
                    Some(answer) => json!({"handled":true,"answer":answer}),
                    None => json!({"handled":false,"answer":""}),
                })
            }
            "control" => {
                let args: Args = match serde_json::from_str(text) {
                    Ok(a) => a,
                    Err(_) => return Ok("Не понял команду.".into()),
                };
                Ok(call.apply(&args).await.into())
            }
            _ => Err("Неизвестная команда Яндекс Умного дома".into()),
        }
    }
}
impl Call<'_> {
    async fn iot(
        &self,
        method: reqwest::Method,
        path: &[&str],
        body: Option<Value>,
    ) -> Result<Value, String> {
        if self.state.token.is_empty() {
            return Err("Яндекс Умный дом не подключён".into());
        }
        let mut url = self.home.base.clone();
        url.path_segments_mut()
            .map_err(|_| "Неверный адрес API".to_string())?
            .pop_if_empty()
            .extend(path);
        let tries = if method == reqwest::Method::GET { 3 } else { 1 };
        let mut response = None;
        for attempt in 0..tries {
            let mut req = self
                .http
                .request(method.clone(), url.clone())
                .bearer_auth(&self.state.token)
                .header("Content-Type", "application/json");
            if let Some(v) = &body {
                req = req.json(v);
            }
            match req.send().await {
                Ok(r) => {
                    response = Some(r);
                    break;
                }
                Err(_) if attempt + 1 < tries => continue,
                Err(_) => return Err("Не удалось связаться с Яндекс Умным домом".into()),
            }
        }
        let mut response = response.ok_or("Яндекс не ответил")?;
        if !response.status().is_success() {
            return Err(format!("Яндекс ответил {}", response.status().as_u16()));
        }
        let mut bytes = Vec::new();
        while let Some(chunk) = response
            .chunk()
            .await
            .map_err(|_| "Ошибка чтения ответа Яндекса".to_string())?
        {
            if bytes.len() + chunk.len() > 4 * 1024 * 1024 {
                return Err("Ответ Яндекса больше 4 МиБ".into());
            }
            bytes.extend_from_slice(&chunk);
        }
        let v: Value = serde_json::from_slice(&bytes)
            .map_err(|_| "Яндекс вернул неверный JSON".to_string())?;
        if v["status"] != "ok" {
            return Err("Яндекс не подтвердил запрос".into());
        }
        Ok(v)
    }
    async fn catalog(&self, force: bool) -> Result<Arc<Catalog>, String> {
        if self.state.token.is_empty() {
            return Err("Яндекс Умный дом не подключён".into());
        }
        if !force {
            if let Some(c) = self.state.cached(true) {
                return Ok(c);
            }
        }
        let _fetch = self.state.fetch.lock().await;
        if !force {
            if let Some(c) = self.state.cached(true) {
                return Ok(c);
            }
        }
        let epoch = self.state.epoch.load(Ordering::SeqCst);
        let result = self
            .iot(reqwest::Method::GET, &["user", "info"], None)
            .await
            .and_then(|v| {
                serde_json::from_value::<Catalog>(v)
                    .map_err(|_| "Неверный каталог Яндекс Умного дома".into())
            });
        match result {
            Ok(c) => {
                let c = Arc::new(c);
                let mut cache = self.state.cache.lock().unwrap();
                if self.state.epoch.load(Ordering::SeqCst) == epoch {
                    *cache = Some((Instant::now(), c.clone()));
                }
                Ok(c)
            }
            Err(e) => {
                if !force {
                    self.state.cached(false).ok_or(e)
                } else {
                    Err(e)
                }
            }
        }
    }
    async fn context(&self) -> String {
        let Ok(Ok(c)) = tokio::time::timeout(Duration::from_secs(4), self.catalog(false)).await
        else {
            return fallback(&self.room, self.alice);
        };
        let rooms: std::collections::BTreeMap<_, _> = c
            .rooms
            .iter()
            .map(|r| (r.id.as_str(), r.name.as_str()))
            .collect();
        let mut text = "\n\nУмный дом хозяина (в инструмент smart_home передавай id):".to_string();
        let mut lines = vec![];
        let mut speaker_room = String::new();
        for d in &c.devices {
            if d.kind.starts_with("devices.types.smart_speaker") {
                if let Some(room) = rooms.get(d.room.as_str()) {
                    speaker_room = room.to_string();
                }
                continue;
            }
            if d.kind.starts_with("devices.types.sensor") {
                continue;
            }
            lines.push(format!(
                "\n- {} «{}» ({}, {}): {}",
                d.id,
                d.name,
                d.kind.trim_start_matches("devices.types."),
                rooms.get(d.room.as_str()).copied().unwrap_or("—"),
                d.describe()
            ));
        }
        lines.sort();
        text += &lines.join("");
        for g in &c.groups {
            text += &format!("\n- группа {} «{}»", g.id, g.name);
        }
        for s in c.scenarios.iter().filter(|s| s.is_active != Some(false)) {
            text += &format!(
                "\n- сценарий {} «{}», фразы: {}",
                s.id,
                s.name,
                s.phrases().join(" / ")
            );
        }
        if !self.room.is_empty() {
            speaker_room = self.room.clone();
        }
        if speaker_room.is_empty() {
            speaker_room = "Зал".into();
        }
        text + &room_line(&speaker_room)
    }
}
fn catalog_view(c: &Catalog) -> Value {
    json!({"rooms":c.rooms.iter().map(|r|json!({"id":r.id,"name":r.name})).collect::<Vec<_>>(),"devices":c.devices.iter().map(|d|json!({"id":d.id,"name":d.name,"room":d.room,"type":d.kind,"description":d.describe()})).collect::<Vec<_>>(),"scenarios":c.scenarios.iter().map(|s|json!({"id":s.id,"name":s.name,"is_active":s.is_active,"phrases":s.phrases()})).collect::<Vec<_>>()})
}
fn norm(s: &str) -> String {
    s.to_lowercase()
        .replace('ё', "е")
        .chars()
        .map(|c| {
            if c.is_alphanumeric() || c.is_whitespace() {
                c
            } else {
                ' '
            }
        })
        .collect::<String>()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
}
fn bare(s: &str) -> String {
    let n = norm(s);
    let mut words: Vec<_> = n.split_whitespace().collect();
    while words
        .first()
        .is_some_and(|w| ["эй", "слушай", "так", "ну", "джарвис", "jarvis"].contains(w))
    {
        words.remove(0);
    }
    words.join(" ")
}
fn room_line(room: &str) -> String {
    format!(
        "\nКолонка стоит в комнате «{room}». Если хозяин не назвал комнату («выключи свет», «включи кондиционер») — имеется в виду эта комната; «весь свет» — все лампы и выключатели света во всём доме."
    )
}
fn fallback(room: &str, alice: bool) -> String {
    let mut s: String = if alice {
        "\nСписок устройств Яндекса сейчас недоступен: команды умного дома передавай Алисе инструментом alice_command.".into()
    } else {
        "\nСписок устройств Яндекса сейчас недоступен. Не утверждай, что действия выполнены.".into()
    };
    if !room.is_empty() {
        s += &room_line(room);
    }
    s
}
