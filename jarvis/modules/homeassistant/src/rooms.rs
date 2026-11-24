use super::*;
use futures_util::{SinkExt, StreamExt};
use std::collections::BTreeMap;
use tokio_tungstenite::{
    MaybeTlsStream, WebSocketStream, connect_async_with_config,
    tungstenite::{Message, protocol::WebSocketConfig},
};

#[derive(Deserialize)]
pub(crate) struct MapRoom {
    pub id: Value,
    pub name: String,
}
#[derive(Deserialize)]
pub(crate) struct MapRooms {
    pub rooms: Vec<MapRoom>,
}
pub(crate) fn parse_rooms(e: &Entity) -> Option<MapRooms> {
    let value = if e.attributes.get("rooms").is_some() {
        json!({"rooms":e.attributes["rooms"]})
    } else {
        serde_json::from_str(&e.state).ok()?
    };
    let rooms: MapRooms = serde_json::from_value(value).ok()?;
    (!rooms.rooms.is_empty()).then_some(rooms)
}
type Ws = WebSocketStream<MaybeTlsStream<tokio::net::TcpStream>>;
async fn read(ws: &mut Ws) -> Result<Value, String> {
    while let Some(msg) = ws.next().await {
        match msg.map_err(|_| "Соединение с реестром Home Assistant прервано".to_string())?
        {
            Message::Text(s) => {
                return serde_json::from_str(&s)
                    .map_err(|_| "Неверный ответ реестра Home Assistant".into());
            }
            Message::Binary(s) => {
                return serde_json::from_slice(&s)
                    .map_err(|_| "Неверный ответ реестра Home Assistant".into());
            }
            Message::Ping(b) => ws
                .send(Message::Pong(b))
                .await
                .map_err(|_| "Ошибка связи Home Assistant".to_string())?,
            Message::Close(_) => break,
            _ => {}
        }
    }
    Err("Home Assistant закрыл соединение с реестром".into())
}
impl Client {
    pub(crate) async fn device_entities(&self) -> Result<BTreeMap<String, String>, String> {
        let base = validate_url(&self.cfg.url)?;
        let mut u = reqwest::Url::parse(&format!("{base}/api/websocket"))
            .map_err(|_| "Неверный адрес Home Assistant".to_string())?;
        let scheme = if u.scheme() == "https" { "wss" } else { "ws" };
        u.set_scheme(scheme)
            .map_err(|_| "Неверная схема WebSocket".to_string())?;
        let ws_cfg = WebSocketConfig::default()
            .max_message_size(Some(8 * 1024 * 1024))
            .max_frame_size(Some(8 * 1024 * 1024));
        let (mut ws, _) = connect_async_with_config(u.as_str(), Some(ws_cfg), false)
            .await
            .map_err(|_| "Не удалось получить список устройств Home Assistant".to_string())?;
        if read(&mut ws).await?["type"] != "auth_required" {
            return Err("Неверное приветствие Home Assistant".into());
        }
        ws.send(Message::Text(
            json!({"type":"auth","access_token":self.cfg.token})
                .to_string()
                .into(),
        ))
        .await
        .map_err(|_| "Не удалось отправить вход Home Assistant".to_string())?;
        if read(&mut ws).await?["type"] != "auth_ok" {
            return Err("Home Assistant не принял вход для списка устройств".into());
        }
        ws.send(Message::Text(
            json!({"id":1,"type":"config/entity_registry/list"})
                .to_string()
                .into(),
        ))
        .await
        .map_err(|_| "Не удалось запросить реестр устройств".to_string())?;
        let result = loop {
            let v = read(&mut ws).await?;
            if v["id"] == 1 {
                break v;
            }
        };
        if result["success"] != true {
            return Err("Home Assistant не вернул реестр устройств".into());
        }
        let rows = result["result"]
            .as_array()
            .ok_or("Неверный реестр устройств")?;
        let mut devices = BTreeMap::new();
        for e in rows {
            if let (Some(id), Some(device)) = (e["entity_id"].as_str(), e["device_id"].as_str()) {
                devices.insert(id.into(), device.into());
            }
        }
        Ok(devices)
    }
    pub(crate) async fn discover_rooms(&self) -> Result<Vec<Room>, String> {
        let entities = self.catalog(true).await?;
        let devices = self.device_entities().await?;
        Ok(discover(&entities, &devices))
    }
    pub(crate) async fn stop_base(&self, vacuum: &str, entities: &[Entity]) {
        let Ok(devices) = self.device_entities().await else {
            return;
        };
        let Some(device) = devices.get(vacuum).filter(|d| !d.is_empty()) else {
            return;
        };
        let same = |e: &&Entity| devices.get(&e.entity_id) == Some(device);
        let mut action = "stop_working";
        for e in entities
            .iter()
            .filter(same)
            .filter(|e| e.entity_id.contains("_base_station_working_status_"))
        {
            let state: Value = serde_json::from_str(&e.state).unwrap_or_default();
            match state["mode"].as_i64() {
                Some(0) => return,
                Some(1) => action = "stop_dry",
                Some(3) => action = "stop_mop_wash",
                _ => {}
            }
        }
        for e in entities
            .iter()
            .filter(same)
            .filter(|e| e.domain() == "button" && e.entity_id.contains(&format!("_{action}_a_")))
        {
            let _ = self
                .service("button", "press", json!({"entity_id":e.entity_id}))
                .await;
        }
    }
}
pub(crate) fn discover(entities: &[Entity], devices: &BTreeMap<String, String>) -> Vec<Room> {
    let mut bindings = vec![];
    for v in entities.iter().filter(|e| e.domain() == "vacuum") {
        let Some(device) = devices.get(&v.entity_id).filter(|d| !d.is_empty()) else {
            continue;
        };
        let siblings: Vec<_> = entities
            .iter()
            .filter(|e| devices.get(&e.entity_id) == Some(device))
            .collect();
        let Some(info) = siblings.iter().find(|e| {
            e.domain() == "sensor"
                && e.entity_id.contains("room_information")
                && parse_rooms(e).is_some()
        }) else {
            continue;
        };
        let Some(action) = siblings
            .iter()
            .find(|e| e.domain() == "notify" && e.entity_id.contains("start_vacuum_room_sweep"))
            .or_else(|| {
                siblings.iter().find(|e| {
                    e.domain() == "text" && e.entity_id.contains("start_vacuum_room_sweep")
                })
            })
        else {
            continue;
        };
        let siid = action
            .entity_id
            .split_once("_a_")
            .and_then(|(_, s)| s.split('_').next())
            .unwrap_or("");
        let result = siblings
            .iter()
            .find(|e| {
                !siid.is_empty()
                    && e.domain() == "sensor"
                    && e.entity_id.contains(&format!("_action_result_p_{siid}_"))
            })
            .map(|e| e.entity_id.clone())
            .unwrap_or_default();
        for r in parse_rooms(info).unwrap().rooms {
            let id = match r.id {
                Value::String(s) => s,
                Value::Number(n) => n.to_string(),
                _ => continue,
            };
            if r.name.is_empty() || id.is_empty() {
                continue;
            }
            let (name, aliases) = room_name(&r.name);
            bindings.push(Room {
                name,
                aliases,
                vacuum: v.entity_id.clone(),
                entity: action.entity_id.clone(),
                room_ids: id,
                info_entity: info.entity_id.clone(),
                info_room: r.name,
                result_entity: result.clone(),
            });
        }
    }
    bindings
}
fn room_name(s: &str) -> (String, Vec<String>) {
    let name = match norm(s).as_str() {
        "living room" => "Гостиная",
        "bedroom" => "Спальня",
        "kitchen" => "Кухня",
        "corridor" | "hallway" => "Коридор",
        "bathroom" => "Ванная",
        "toilet" => "Туалет",
        "office" | "study" => "Кабинет",
        _ => return (s.into(), vec![]),
    };
    let mut aliases = vec![s.into()];
    if name == "Гостиная" {
        aliases.push("зал".into());
    }
    (name.into(), aliases)
}
