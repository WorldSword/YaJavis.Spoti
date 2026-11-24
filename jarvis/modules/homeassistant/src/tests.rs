use super::*;
use axum::{
    Json, Router,
    extract::{
        Path, State,
        ws::{Message, WebSocket, WebSocketUpgrade},
    },
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, Ordering};
use tokio::task::JoinHandle;

#[derive(Clone)]
struct Fake {
    entities: Arc<Mutex<Vec<Entity>>>,
    posts: Arc<Mutex<Vec<(String, Value)>>>,
    devices: Arc<Mutex<BTreeMap<String, String>>>,
    reject: bool,
    ack: Arc<AtomicBool>,
}
struct Server {
    url: String,
    task: JoinHandle<()>,
    state: Fake,
}
impl Drop for Server {
    fn drop(&mut self) {
        self.task.abort();
    }
}
fn entity(id: &str, state: &str) -> Entity {
    Entity {
        entity_id: id.into(),
        state: state.into(),
        ..Default::default()
    }
}
async fn mock_ws(upgrade: WebSocketUpgrade, State(s): State<Fake>) -> Response {
    upgrade.on_upgrade(move |mut ws: WebSocket| async move {
        ws.send(Message::Text(
            json!({"type":"auth_required"}).to_string().into(),
        ))
        .await
        .unwrap();
        let Some(Ok(Message::Text(auth))) = ws.recv().await else {
            return;
        };
        let auth: Value = serde_json::from_str(&auth).unwrap();
        assert_eq!(auth["access_token"], "test-token");
        ws.send(Message::Text(json!({"type":"auth_ok"}).to_string().into()))
            .await
            .unwrap();
        let Some(Ok(Message::Text(req))) = ws.recv().await else {
            return;
        };
        let req: Value = serde_json::from_str(&req).unwrap();
        assert_eq!(req["type"], "config/entity_registry/list");
        let result: Vec<Value> = s
            .devices
            .lock()
            .unwrap()
            .iter()
            .map(|(id, device)| json!({"entity_id":id,"device_id":device}))
            .collect();
        let _ = ws
            .send(Message::Text(
                json!({"id":req["id"],"success":true,"result":result})
                    .to_string()
                    .into(),
            ))
            .await;
    })
}
fn auth(h: &HeaderMap) -> bool {
    h.get("authorization")
        .is_some_and(|v| v == "Bearer test-token")
}
async fn states(State(s): State<Fake>, h: HeaderMap) -> Response {
    if s.reject || !auth(&h) {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    Json(s.entities.lock().unwrap().clone()).into_response()
}
async fn state(State(s): State<Fake>, Path(id): Path<String>, h: HeaderMap) -> Response {
    if !auth(&h) {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    match s
        .entities
        .lock()
        .unwrap()
        .iter()
        .find(|e| e.entity_id == id)
        .cloned()
    {
        Some(e) => Json(e).into_response(),
        None => StatusCode::NOT_FOUND.into_response(),
    }
}
async fn service(
    State(s): State<Fake>,
    Path((domain, action)): Path<(String, String)>,
    h: HeaderMap,
    Json(v): Json<Value>,
) -> Response {
    if !auth(&h) {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    s.posts
        .lock()
        .unwrap()
        .push((format!("{domain}/{action}"), v.clone()));
    let mut entities = s.entities.lock().unwrap();
    if let Some(e) = entities
        .iter_mut()
        .find(|e| e.entity_id == v["entity_id"].as_str().unwrap_or(""))
    {
        e.state = match action.as_str() {
            "turn_on" => "on",
            "turn_off" => "off",
            "start" => "cleaning",
            "pause" => "paused",
            "stop" => "idle",
            "return_to_base" => "returning",
            _ => &e.state,
        }
        .into();
        e.last_updated = jiff::Timestamp::now().to_string();
    }
    if action == "send_message" && s.ack.load(Ordering::Relaxed) {
        if let Some(e) = entities.iter_mut().find(|e| e.entity_id == "sensor.result") {
            e.state = r#"{"code":0}"#.into();
            e.last_reported = jiff::Timestamp::now().to_string();
        }
    }
    Json(json!([])).into_response()
}
async fn server(entities: Vec<Entity>) -> Server {
    let state = Fake {
        entities: Arc::new(Mutex::new(entities)),
        posts: Arc::default(),
        devices: Arc::default(),
        reject: false,
        ack: Arc::new(AtomicBool::new(true)),
    };
    let app = Router::new()
        .route("/api/states", get(states))
        .route("/api/states/{id}", get(self::state))
        .route("/api/services/{domain}/{action}", post(service))
        .route("/api/websocket", get(mock_ws))
        .with_state(state.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    Server { url, task, state }
}
fn params(s: &Server, op: &str, args: Value, rooms: Vec<Room>) -> Value {
    json!({"op":op,"config":{"url":s.url,"token":"test-token","rooms":rooms},"args":args})
}
fn binding() -> Room {
    Room {
        name: "Кухня".into(),
        aliases: vec!["kitchen".into()],
        vacuum: "vacuum.robot".into(),
        entity: "notify.room_sweep".into(),
        room_ids: "7".into(),
        info_entity: "sensor.room_information".into(),
        info_room: "Kitchen".into(),
        result_entity: "sensor.result".into(),
    }
}

#[test]
fn urls_and_room_names() {
    assert_eq!(
        validate_url(" http://localhost:8123/base/ ").unwrap(),
        "http://localhost:8123/base"
    );
    for s in [
        "file:///tmp/x",
        "http://u:pass@localhost",
        "http://localhost/?x=1",
        "http://localhost/#x",
    ] {
        assert!(validate_url(s).is_err(), "{s}");
    }
    assert!(commands::room_mention("убери на кухне", "Кухня"));
    assert!(!commands::room_mention("убери в кухонном шкафу", "Кухня"));
    assert!(!valid_entity("light.x/../../services"));
}
#[tokio::test]
async fn controls_confirmed_states_and_refuses_unknown_rooms() {
    let server = server(vec![
        entity("light.test", "off"),
        entity("vacuum.robot", "docked"),
    ])
    .await;
    let ha = HomeAssistant::new().unwrap();
    let result = ha
        .request(params(
            &server,
            "control",
            json!(r#"{"entity_id":"light.test","action":"turn_on"}"#),
            vec![],
        ))
        .await
        .unwrap();
    assert_eq!(result, "light.test: включено.");
    assert_eq!(server.state.posts.lock().unwrap().len(), 1);
    let result = ha
        .request(params(
            &server,
            "control",
            json!(r#"{"entity_id":"vacuum.robot","action":"clean_room","room":"Неизвестная"}"#),
            vec![],
        ))
        .await
        .unwrap();
    assert!(result.as_str().unwrap().contains("ещё не настроена"));
    assert_eq!(server.state.posts.lock().unwrap().len(), 1);
    let result = ha
        .request(params(
            &server,
            "command",
            json!("Джарвис, убери спальню!"),
            vec![],
        ))
        .await
        .unwrap();
    assert!(
        result
            .as_str()
            .unwrap()
            .contains("Всю квартиру вместо неё не запускаю")
    );
    assert_eq!(server.state.posts.lock().unwrap().len(), 1);
    assert_eq!(
        ha.request(params(
            &server,
            "intent",
            json!("Джарвис, останови пылесос!"),
            vec![]
        ))
        .await
        .unwrap(),
        true
    );
    assert_eq!(
        ha.request(params(
            &server,
            "intent",
            json!("Расскажи про пылесос"),
            vec![]
        ))
        .await
        .unwrap(),
        false
    );
}
#[tokio::test]
async fn room_ids_are_refreshed_and_success_requires_fresh_ack() {
    let mut info = entity("sensor.room_information", "unknown");
    info.attributes = json!({"rooms":[{"id":23,"name":"Kitchen"}]});
    let server = server(vec![
        entity("vacuum.robot", "docked"),
        entity("notify.room_sweep", "unknown"),
        info,
        entity("sensor.result", r#"{"code":9}"#),
    ])
    .await;
    let ha = HomeAssistant::new().unwrap();
    let result = ha
        .request(params(
            &server,
            "command",
            json!("Джарвис, убери на кухне!"),
            vec![binding()],
        ))
        .await
        .unwrap();
    assert!(
        result.as_str().unwrap().contains("подтвердил команду"),
        "{result}"
    );
    let posts = server.state.posts.lock().unwrap();
    assert_eq!(posts[0].0, "notify/send_message");
    assert_eq!(posts[0].1["message"], "'23'");
    drop(posts);
    server.state.ack.store(false, Ordering::Relaxed);
    {
        let mut entities = server.state.entities.lock().unwrap();
        let result = entities
            .iter_mut()
            .find(|e| e.entity_id == "sensor.result")
            .unwrap();
        result.state = r#"{"code":0}"#.into();
        result.last_updated = "2000-01-01T00:00:00Z".into();
        result.last_reported = result.last_updated.clone();
    }
    let result = ha
        .request(params(
            &server,
            "control",
            json!(r#"{"action":"clean_room","room":"Кухня"}"#),
            vec![binding()],
        ))
        .await
        .unwrap();
    assert!(
        result.as_str().unwrap().contains("Жду подтверждения"),
        "старый успех не подтверждает новую команду: {result}"
    );
    // Пропавшая комната не подменяется прежним номером зоны.
    server
        .state
        .entities
        .lock()
        .unwrap()
        .iter_mut()
        .find(|e| e.entity_id == "sensor.room_information")
        .unwrap()
        .attributes = json!({"rooms":[{"id":4,"name":"Bedroom"}]});
    let result = ha
        .request(params(
            &server,
            "control",
            json!(r#"{"action":"clean_room","room":"Кухня"}"#),
            vec![binding()],
        ))
        .await
        .unwrap();
    assert!(result.as_str().unwrap().contains("изменилась"));
    assert_eq!(server.state.posts.lock().unwrap().len(), 2);
}
#[tokio::test]
async fn discovers_rooms_from_same_device_via_websocket() {
    let mut info = entity("sensor.robot_room_information", "unknown");
    info.attributes = json!({"rooms":[{"id":23,"name":"Kitchen"}]});
    let server = server(vec![
        entity("vacuum.robot", "docked"),
        info,
        entity("notify.robot_start_vacuum_room_sweep_a_7_1", "unknown"),
        entity("notify.other_start_vacuum_room_sweep_a_7_1", "unknown"),
        entity("sensor.robot_action_result_p_7_1", "unknown"),
    ])
    .await;
    for e in server.state.entities.lock().unwrap().iter() {
        server.state.devices.lock().unwrap().insert(
            e.entity_id.clone(),
            if e.entity_id.contains("other") {
                "other"
            } else {
                "robot"
            }
            .into(),
        );
    }
    let ha = HomeAssistant::new().unwrap();
    let result = ha
        .request(params(&server, "rooms", Value::Null, vec![]))
        .await
        .unwrap();
    assert_eq!(result[0]["name"], "Кухня");
    assert_eq!(result[0]["room_ids"], "23");
    assert_eq!(
        result[0]["result_entity"],
        "sensor.robot_action_result_p_7_1"
    );
    assert_eq!(
        result[0]["entity"],
        "notify.robot_start_vacuum_room_sweep_a_7_1"
    );
}

#[tokio::test]
async fn stops_robot_and_only_its_own_washing_base() {
    let server = server(vec![
        entity("vacuum.robot", "cleaning"),
        entity(
            "sensor.robot_base_station_working_status_1",
            r#"{"mode":3}"#,
        ),
        entity("button.robot_stop_mop_wash_a_1", "unknown"),
        entity("button.other_stop_mop_wash_a_1", "unknown"),
    ])
    .await;
    for e in server.state.entities.lock().unwrap().iter() {
        server.state.devices.lock().unwrap().insert(
            e.entity_id.clone(),
            if e.entity_id.contains("other") {
                "other"
            } else {
                "robot"
            }
            .into(),
        );
    }
    let ha = HomeAssistant::new().unwrap();
    let result = ha
        .request(params(
            &server,
            "command",
            json!("Останови пылесос"),
            vec![],
        ))
        .await
        .unwrap();
    assert_eq!(result, "vacuum.robot: ожидает.");
    let posts = server.state.posts.lock().unwrap();
    assert!(
        posts
            .iter()
            .any(|(op, v)| op == "button/press"
                && v["entity_id"] == "button.robot_stop_mop_wash_a_1")
    );
    assert!(
        !posts
            .iter()
            .any(|(_, v)| v["entity_id"] == "button.other_stop_mop_wash_a_1")
    );
    assert_eq!(posts.last().unwrap().0, "vacuum/stop");
}
#[tokio::test]
async fn validates_config_and_preserves_token_only_for_same_server() {
    let server = server(vec![entity("light.test", "off")]).await;
    let ha = HomeAssistant::new().unwrap();
    let mut p = params(&server, "settings.validate", Value::Null, vec![]);
    p["config"]["token"] = "".into();
    p["previous"] = json!({"url":server.url,"token":"test-token"});
    let validated = ha.request(p.clone()).await.unwrap();
    assert_eq!(validated["token"], "test-token");
    p["previous"]["url"] = "http://other.invalid".into();
    assert!(ha.request(p).await.unwrap_err().contains("токен"));
    let mut room = binding();
    room.entity = "script.missing".into();
    assert!(
        ha.request(params(
            &server,
            "settings.validate",
            Value::Null,
            vec![room]
        ))
        .await
        .is_err()
    );
}
#[tokio::test]
async fn caches_states_but_drops_cache_on_config_changes() {
    let server = server(vec![entity("light.test", "off")]).await;
    let ha = HomeAssistant::new().unwrap();
    assert_eq!(
        ha.request(params(&server, "catalog", false.into(), vec![]))
            .await
            .unwrap()[0]["state"],
        "off"
    );
    server.state.entities.lock().unwrap()[0].state = "on".into();
    assert_eq!(
        ha.request(params(&server, "catalog", false.into(), vec![]))
            .await
            .unwrap()[0]["state"],
        "off"
    );
    assert_eq!(
        ha.request(params(&server, "catalog", true.into(), vec![]))
            .await
            .unwrap()[0]["state"],
        "on"
    );
    let mut bad = params(&server, "catalog", false.into(), vec![]);
    bad["config"]["token"] = "invalid".into();
    assert!(ha.request(bad).await.is_err());
}
