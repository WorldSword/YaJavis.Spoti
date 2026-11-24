//! Home Assistant: самостоятельный модуль, без зависимости от ядра и Go.
mod commands;
mod control;
mod rooms;
#[cfg(test)]
mod tests;

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq)]
#[serde(default)]
pub struct Room {
    pub name: String,
    pub aliases: Vec<String>,
    pub vacuum: String,
    pub entity: String,
    pub room_ids: String,
    pub info_entity: String,
    pub info_room: String,
    pub result_entity: String,
}
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq)]
#[serde(default)]
pub struct Config {
    pub url: String,
    pub token: String,
    pub rooms: Vec<Room>,
}
impl Config {
    fn ready(&self) -> bool {
        !self.url.is_empty() && !self.token.is_empty()
    }
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct Entity {
    pub entity_id: String,
    pub state: String,
    pub attributes: Value,
    pub last_updated: String,
    pub last_reported: String,
}
impl Entity {
    fn name(&self) -> &str {
        self.attributes["friendly_name"]
            .as_str()
            .unwrap_or(&self.entity_id)
    }
    fn domain(&self) -> &str {
        self.entity_id.split('.').next().unwrap_or("")
    }
    fn newer_than(&self, requested: jiff::Timestamp) -> bool {
        [&self.last_updated, &self.last_reported]
            .iter()
            .any(|s| s.parse::<jiff::Timestamp>().is_ok_and(|v| v > requested))
    }
}
#[derive(Default)]
struct Cache {
    entities: Vec<Entity>,
    fetched: Option<Instant>,
    failed: Option<Instant>,
}
pub(crate) struct Client {
    cfg: Config,
    http: reqwest::Client,
    cache: Mutex<Cache>,
}
pub struct HomeAssistant {
    http: reqwest::Client,
    current: Mutex<Option<Arc<Client>>>,
}

pub fn validate_url(raw: &str) -> Result<String, String> {
    let mut u = reqwest::Url::parse(raw.trim()).map_err(|_| {
        "Укажите адрес Home Assistant, например http://192.168.1.10:8123".to_string()
    })?;
    if !["http", "https"].contains(&u.scheme())
        || u.host_str().is_none()
        || !u.username().is_empty()
        || u.password().is_some()
        || u.query().is_some()
        || u.fragment().is_some()
    {
        return Err("Неверный адрес Home Assistant".into());
    }
    let path = u.path().trim_end_matches('/').to_string();
    u.set_path(&path);
    Ok(u.to_string().trim_end_matches('/').into())
}
impl HomeAssistant {
    pub fn new() -> Result<Self, String> {
        let http = reqwest::Client::builder()
            .no_proxy()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(15))
            .build()
            .map_err(|e| e.to_string())?;
        Ok(Self {
            http,
            current: Mutex::default(),
        })
    }
    pub async fn request(&self, p: Value) -> Result<Value, String> {
        tokio::time::timeout(Duration::from_secs(25), self.handle(p))
            .await
            .map_err(|_| "Home Assistant не ответил вовремя".to_string())?
    }
    async fn handle(&self, p: Value) -> Result<Value, String> {
        let mut cfg: Config = serde_json::from_value(
            p.get("config")
                .filter(|v| !v.is_null())
                .cloned()
                .unwrap_or(json!({})),
        )
        .map_err(|_| "Неверные настройки Home Assistant".to_string())?;
        if p["op"] == "settings.validate" {
            cfg.url = validate_url(&cfg.url)?;
            cfg.token = cfg.token.trim().into();
            let old: Config = serde_json::from_value(p["previous"].clone()).unwrap_or_default();
            if cfg.token.is_empty() && validate_url(&old.url).is_ok_and(|url| url == cfg.url) {
                cfg.token = old.token;
            }
            if cfg.token.is_empty() {
                return Err(
                    "Вставь долгосрочный токен Home Assistant из профиля пользователя".into(),
                );
            }
            if cfg.rooms.len() > 100 {
                return Err("Слишком много комнат".into());
            }
        }
        let c = {
            let mut current = self.current.lock().unwrap();
            if current.as_ref().is_none_or(|c| c.cfg != cfg) {
                *current = Some(Arc::new(Client {
                    cfg,
                    http: self.http.clone(),
                    cache: Mutex::default(),
                }));
            }
            current.as_ref().unwrap().clone()
        };
        let args = &p["args"];
        let text = args.as_str().unwrap_or("");
        match p["op"].as_str().unwrap_or("") {
            "settings.validate" => {
                let entities = c.catalog(true).await?;
                for r in &c.cfg.rooms {
                    let known = |id: &str| entities.iter().any(|e| e.entity_id == id);
                    if r.name.trim().is_empty()
                        || !valid_entity(&r.entity)
                        || !known(&r.entity)
                        || !["script", "notify", "text"]
                            .contains(&r.entity.split('.').next().unwrap_or(""))
                    {
                        return Err(
                            "Выбери существующий сценарий или действие уборки для каждой комнаты"
                                .into(),
                        );
                    }
                    if !r.vacuum.is_empty()
                        && (!r.vacuum.starts_with("vacuum.") || !known(&r.vacuum))
                    {
                        return Err("Выбранный пылесос не найден".into());
                    }
                    if !r.entity.starts_with("script.") && r.room_ids.trim().is_empty() {
                        return Err(
                            "Для действия уборки нужны номера комнат на карте пылесоса".into()
                        );
                    }
                }
                serde_json::to_value(&c.cfg).map_err(|e| e.to_string())
            }
            "catalog" => serde_json::to_value(c.catalog(args.as_bool().unwrap_or(false)).await?)
                .map_err(|e| e.to_string()),
            "rooms" => serde_json::to_value(c.discover_rooms().await?).map_err(|e| e.to_string()),
            "intent" => Ok(c.intent(text).into()),
            "command" => Ok(c.vacuum_command(text).await.into()),
            "control" => Ok(c
                .control(serde_json::from_str(text).unwrap_or(Value::Null))
                .await
                .into()),
            "alive" => {
                let failed = {
                    let cache = c.cache.lock().unwrap();
                    cache.failed.is_some_and(|f| {
                        f.elapsed() < Duration::from_secs(60) && cache.fetched.is_none_or(|s| f > s)
                    })
                };
                if !c.cfg.ready() || failed {
                    return Ok(false.into());
                }
                Ok(
                    tokio::time::timeout(Duration::from_millis(1500), c.catalog(false))
                        .await
                        .is_ok_and(|v| v.is_ok())
                        .into(),
                )
            }
            "context" => Ok(c.context().await.into()),
            "invalidate" => {
                *c.cache.lock().unwrap() = Cache::default();
                Ok(true.into())
            }
            _ => Err("Неизвестная команда Home Assistant".into()),
        }
    }
}
impl Client {
    async fn request(
        &self,
        method: reqwest::Method,
        path: &str,
        body: Option<Value>,
    ) -> Result<Value, String> {
        if self.cfg.token.is_empty() {
            return Err("Home Assistant не подключён".into());
        }
        let base = validate_url(&self.cfg.url)?;
        let mut req = self
            .http
            .request(method, format!("{base}/api/{path}"))
            .bearer_auth(&self.cfg.token);
        if let Some(body) = body {
            req = req.json(&body);
        }
        let mut response = req.send().await.map_err(|_| {
            "Home Assistant недоступен — проверь адрес и включён ли сервер".to_string()
        })?;
        if !response.status().is_success() {
            return Err(format!(
                "Home Assistant ответил {}",
                response.status().as_u16()
            ));
        }
        let mut bytes = Vec::new();
        while let Some(chunk) = response
            .chunk()
            .await
            .map_err(|_| "Ошибка чтения ответа Home Assistant".to_string())?
        {
            if bytes.len() + chunk.len() > 4 * 1024 * 1024 {
                return Err("Ответ Home Assistant больше 4 МиБ".into());
            }
            bytes.extend_from_slice(&chunk);
        }
        if bytes.is_empty() {
            return Ok(Value::Null);
        }
        serde_json::from_slice(&bytes).map_err(|_| "Home Assistant вернул неверный JSON".into())
    }
    async fn catalog(&self, force: bool) -> Result<Vec<Entity>, String> {
        {
            let c = self.cache.lock().unwrap();
            if !force
                && c.fetched
                    .is_some_and(|t| t.elapsed() < Duration::from_secs(30))
            {
                return Ok(c.entities.clone());
            }
        }
        let result = self
            .request(reqwest::Method::GET, "states", None)
            .await
            .and_then(|v| {
                serde_json::from_value::<Vec<Entity>>(v)
                    .map_err(|_| "Неверный список устройств Home Assistant".into())
            });
        let mut c = self.cache.lock().unwrap();
        match result {
            Ok(mut entities) => {
                entities.sort_by(|a, b| a.entity_id.cmp(&b.entity_id));
                c.entities = entities.clone();
                c.fetched = Some(Instant::now());
                Ok(entities)
            }
            Err(e) => {
                c.failed = Some(Instant::now());
                Err(e)
            }
        }
    }
    async fn state(&self, id: &str) -> Result<Entity, String> {
        if !valid_entity(id) {
            return Err("Неверный entity_id".into());
        }
        serde_json::from_value(
            self.request(reqwest::Method::GET, &format!("states/{id}"), None)
                .await?,
        )
        .map_err(|_| "Неверное состояние устройства".into())
    }
    async fn service(&self, domain: &str, service: &str, data: Value) -> Result<(), String> {
        self.request(
            reqwest::Method::POST,
            &format!("services/{domain}/{service}"),
            Some(data),
        )
        .await
        .map(|_| ())
    }
    async fn context(&self) -> String {
        if !self.cfg.ready() {
            return String::new();
        }
        let Ok(Ok(entities)) =
            tokio::time::timeout(Duration::from_secs(3), self.catalog(false)).await
        else {
            return "\nHome Assistant подключён, но сейчас недоступен. Не утверждай, что действия выполнены.".into();
        };
        let mut text="\n\nHome Assistant: используй home_assistant только для entity_id ниже. Уборка комнаты — clean_room и название комнаты; не заменяй её уборкой всей квартиры. Не выдумывай номера комнат.\n".to_string();
        for e in entities
            .iter()
            .filter(|e| {
                [
                    "vacuum", "script", "scene", "light", "switch", "fan", "climate", "cover",
                ]
                .contains(&e.domain())
            })
            .take(80)
        {
            text += &format!("{} «{}»: {}\n", e.entity_id, e.name(), e.state);
        }
        for r in &self.cfg.rooms {
            text += &format!(
                "Доступна уборка комнаты «{}» (пылесос {}).\n",
                r.name, r.vacuum
            );
        }
        text
    }
}
fn valid_entity(id: &str) -> bool {
    let Some((d, n)) = id.split_once('.') else {
        return false;
    };
    !d.is_empty()
        && !n.is_empty()
        && d.bytes()
            .chain(n.bytes())
            .all(|c| c.is_ascii_alphanumeric() || c == b'_')
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
    n.strip_prefix("джарвис ").unwrap_or(&n).to_string()
}
fn state_text(s: &str) -> &str {
    match s {
        "cleaning" => "убирает",
        "docked" => "на базе",
        "returning" => "возвращается на базу",
        "paused" => "на паузе",
        "idle" => "ожидает",
        "on" => "включено",
        "off" => "выключено",
        "unavailable" => "недоступно",
        "unknown" => "состояние пока неизвестно",
        _ => s,
    }
}
