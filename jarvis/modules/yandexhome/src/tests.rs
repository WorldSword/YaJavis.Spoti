use super::*;
use axum::{
    Json, Router,
    body::Bytes,
    extract::{Path, State as AxumState},
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use std::sync::atomic::{AtomicU8, AtomicUsize};
use tokio::task::JoinHandle;
const CATALOG: &str = include_str!("catalog-test.json");
fn catalog() -> Catalog {
    serde_json::from_str(CATALOG).unwrap()
}
#[derive(Clone)]
struct Fake {
    catalog: Arc<Mutex<Value>>,
    posts: Arc<Mutex<Vec<(String, Value)>>>,
    gets: Arc<AtomicUsize>,
    mode: Arc<AtomicU8>,
}
struct Server {
    url: String,
    state: Fake,
    task: JoinHandle<()>,
}
impl Drop for Server {
    fn drop(&mut self) {
        self.task.abort();
    }
}
fn authorized(h: &HeaderMap) -> bool {
    h.get("authorization")
        .is_some_and(|s| s == "Bearer test-token")
}
async fn info(AxumState(s): AxumState<Fake>, h: HeaderMap) -> Response {
    s.gets.fetch_add(1, Ordering::Relaxed);
    if !authorized(&h) {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    Json(s.catalog.lock().unwrap().clone()).into_response()
}
fn ack(id: &str, actions: &[Value]) -> Value {
    json!({"id":id,"capabilities":actions.iter().map(|a|json!({"type":a["type"],"state":{"instance":a["state"]["instance"],"action_result":{"status":"DONE"}}})).collect::<Vec<_>>()})
}
async fn action(
    AxumState(s): AxumState<Fake>,
    Path(path): Path<String>,
    h: HeaderMap,
    body: Bytes,
) -> Response {
    if !authorized(&h) {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    let body: Value = if body.is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(&body).unwrap()
    };
    s.posts.lock().unwrap().push((path.clone(), body.clone()));
    let mode = s.mode.load(Ordering::Relaxed);
    if mode == 7 {
        return StatusCode::INTERNAL_SERVER_ERROR.into_response();
    }
    if mode == 8 {
        return "not-json".into_response();
    }
    if path.starts_with("scenarios/") {
        return Json(json!({"status":if mode==0 {"ok"}else{"error"}})).into_response();
    }
    let mut devices: Vec<Value> = if path == "devices/actions" {
        body["devices"]
            .as_array()
            .unwrap()
            .iter()
            .map(|d| ack(d["id"].as_str().unwrap(), d["actions"].as_array().unwrap()))
            .collect()
    } else {
        let c = s.catalog.lock().unwrap();
        let group = c["groups"]
            .as_array()
            .unwrap()
            .iter()
            .find(|g| path == format!("groups/{}/actions", g["id"].as_str().unwrap()))
            .unwrap();
        group["devices"]
            .as_array()
            .unwrap()
            .iter()
            .map(|id| ack(id.as_str().unwrap(), body["actions"].as_array().unwrap()))
            .collect()
    };
    match mode {
        1 => {
            devices.pop();
        }
        2 => {
            if let Some(d) = devices.first_mut() {
                d["capabilities"].as_array_mut().unwrap().remove(0);
            }
        }
        3 => {
            devices[0]["capabilities"][0]["state"]["action_result"]["status"] = "ERROR".into();
        }
        4 => {
            devices[0]["id"] = "unknown-id".into();
        }
        5 => {
            devices.push(devices[0].clone());
        }
        _ => {}
    }
    Json(json!({"status":if mode==6 {"error"}else{"ok"},"devices":devices})).into_response()
}
async fn server() -> Server {
    let state = Fake {
        catalog: Arc::new(Mutex::new(serde_json::from_str(CATALOG).unwrap())),
        posts: Arc::default(),
        gets: Arc::default(),
        mode: Arc::default(),
    };
    let app = Router::new()
        .route("/v1.0/user/info", get(info))
        .route("/v1.0/{*path}", post(action))
        .with_state(state.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/v1.0/", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    Server { url, state, task }
}
fn home(s: &Server) -> YandexHome {
    YandexHome {
        base: reqwest::Url::parse(&s.url).unwrap(),
        state: Mutex::default(),
        clients: Mutex::default(),
    }
}
fn params(op: &str, args: Value) -> Value {
    json!({"op":op,"args":args,"token":"test-token","room":"Зал","alice":false})
}
async fn apply(h: &YandexHome, a: Value) -> Value {
    h.request(params("control", a.to_string().into()), json!({}))
        .await
        .unwrap()
}

#[test]
fn plans_existing_commands_and_aliases() {
    let c = catalog();
    for (text, want) in [
        ("Джарвис, выключи свет.", "off:l1"),
        ("включи свет на кухне", "on:l3"),
        ("выключи весь свет", "off:l1,l2,l3,l4"),
        ("выключи свет во всей квартире", "off:l1,l2,l3,l4"),
        ("Выключи стол", "off:l2"),
        ("включи ленту", "on:l4"),
        ("включи ленту в спальне", "on:l4"),
        ("выключи розетку", "off:s1"),
        ("запусти сценарий я дома", "scenario:sc1"),
        ("запусти пылесос", "on:v1"),
        ("останови пылесос", "off:v1"),
        ("выключи кондей", "off:ac"),
        ("включи подсветку кровати", "on:l4"),
    ] {
        let n = bare(text);
        let m = super::fast::COMMAND.captures(&n).unwrap();
        let p = fast::plan(
            &c,
            m.get(1).unwrap().as_str(),
            m.get(2).unwrap().as_str(),
            "",
        )
        .unwrap();
        let result = if !p.scenario_id.is_empty() {
            format!("scenario:{}", p.scenario_id)
        } else {
            format!(
                "{}:{}",
                if p.on == Some(true) { "on" } else { "off" },
                p.device_ids.join(",")
            )
        };
        assert_eq!(result, want, "{text}");
    }
}
#[test]
fn honors_explicit_rooms_and_refuses_ambiguous_devices() {
    let mut c = catalog();
    let mut duplicate = c.devices.iter().find(|d| d.id == "s1").unwrap().clone();
    duplicate.id = "s2".into();
    duplicate.room = "r3".into();
    c.devices.push(duplicate);
    assert!(fast::plan(&c, "включи", "розетку", "").is_none());
    assert_eq!(
        fast::plan(&c, "включи", "розетку на кухне", "")
            .unwrap()
            .device_ids,
        ["s1"]
    );
    assert!(
        fast::plan(&c, "включи", "ленту на кухне", "").is_none(),
        "точное имя не должно обходить выбранную комнату"
    );
    assert!(fast::plan(&c, "включи", "свет в лаборатории", "").is_none());
    assert_eq!(
        fast::plan(&c, "включи", "свет", "Кухня")
            .unwrap()
            .device_ids,
        ["l3"]
    );
    assert!(fast::plan(&c, "включи", "свет", "Несуществующая").is_none());
}
#[test]
fn actions_follow_capabilities_ranges_and_color_models() {
    let c = catalog();
    let d = c.devices.iter().find(|d| d.id == "l4").unwrap();
    let a: Args = serde_json::from_value(json!({"color":"#FF0000","temperature_k":9999})).unwrap();
    let actions = model::actions_for(d, &a);
    assert_eq!(actions[0]["state"]["value"], true);
    assert_eq!(actions[1]["state"]["value"], 0xFF0000);
    assert_eq!(actions[2]["state"]["value"], 6500);
    let mut hsv = d.clone();
    hsv.capabilities[1].parameters["color_model"] = "hsv".into();
    let acts = model::actions_for(&hsv, &a);
    assert_eq!(acts[1]["state"]["value"], json!({"h":0,"s":100,"v":100}));
    let ac = c.devices.iter().find(|d| d.id == "ac").unwrap();
    let a:Args=serde_json::from_value(json!({"ranges":{"temperature":100},"modes":{"thermostat":"cool","unknown":"x"},"toggles":{"oscillation":true}})).unwrap();
    let acts = model::actions_for(ac, &a);
    assert_eq!(acts.len(), 3);
    assert_eq!(acts[0]["state"]["value"], 30.0);
    assert!(
        model::actions_for(
            ac,
            &serde_json::from_value(json!({"modes":{"thermostat":"invalid"}})).unwrap()
        )
        .is_empty()
    );
}
#[tokio::test]
async fn fast_commands_scenarios_colors_and_context_use_real_protocol() {
    let s = server().await;
    let h = home(&s);
    assert_eq!(
        h.request(params("has-catalog", Value::Null), json!({}))
            .await
            .unwrap(),
        false
    );
    let answer = h
        .request(params("fast", "Джарвис, выключи свет!".into()), json!({}))
        .await
        .unwrap();
    assert_eq!(answer["answer"], "Готово.");
    let posts = s.state.posts.lock().unwrap();
    assert_eq!(posts[0].0, "devices/actions");
    assert_eq!(posts[0].1["devices"][0]["id"], "l1");
    drop(posts);
    let answer = h
        .request(params("fast", "Я вернулся".into()), json!({}))
        .await
        .unwrap();
    assert_eq!(answer["answer"], "Готово.");
    assert_eq!(
        s.state.posts.lock().unwrap().last().unwrap().0,
        "scenarios/sc1/actions"
    );
    let answer = h
        .request(
            params("fast", "Джарвис, сделай ленту красной".into()),
            json!({}),
        )
        .await
        .unwrap();
    assert_eq!(answer["answer"], "Готово.");
    let posts = s.state.posts.lock().unwrap();
    let acts = &posts.last().unwrap().1["devices"][0]["actions"];
    assert_eq!(acts[1]["state"]["value"], 0xFF0000);
    drop(posts);
    let context = h
        .request(params("context", Value::Null), json!({}))
        .await
        .unwrap();
    assert!(
        context
            .as_str()
            .unwrap()
            .contains("Колонка стоит в комнате «Зал»")
    );
    assert_eq!(
        h.request(params("has-catalog", Value::Null), json!({}))
            .await
            .unwrap(),
        true
    );
}
#[tokio::test]
async fn missing_failed_and_mismatched_confirmations_are_not_success() {
    let s = server().await;
    for mode in 1..=8 {
        s.state.mode.store(mode, Ordering::Relaxed);
        let h = home(&s);
        let before = s.state.posts.lock().unwrap().len();
        let result = apply(&h, json!({"device_ids":["l4"],"color":"#00ff00"})).await;
        assert_ne!(result, "Готово.", "mode {mode}");
        assert_eq!(
            s.state.posts.lock().unwrap().len(),
            before + 1,
            "изменяющий запрос не должен повторяться"
        );
    }
}
#[tokio::test]
async fn group_success_requires_all_members() {
    let s = server().await;
    for mode in [0, 1, 3, 5] {
        s.state.mode.store(mode, Ordering::Relaxed);
        let result = apply(&home(&s), json!({"device_ids":["g1"],"on":true})).await;
        assert_eq!(result == "Готово.", mode == 0, "mode {mode}: {result}");
    }
}
#[tokio::test]
async fn unknown_disabled_and_unsupported_actions_do_not_run() {
    let s = server().await;
    let h = home(&s);
    for a in [
        json!({"scenario_id":"disabled"}),
        json!({"scenario_id":"../unknown"}),
        json!({"device_ids":["unknown"],"on":true}),
        json!({"device_ids":["ac"],"modes":{"thermostat":"bad"}}),
        json!({"device_ids":["l1"],"color":"invalid"}),
    ] {
        assert_ne!(apply(&h, a).await, "Готово.");
    }
    assert!(s.state.posts.lock().unwrap().is_empty());
    assert_eq!(
        h.request(params("scenario-match", "Не запускать".into()), json!({}))
            .await
            .unwrap(),
        false
    );
    assert_eq!(
        h.request(
            params("fast", "Расскажи как включить свет".into()),
            json!({})
        )
        .await
        .unwrap()["handled"],
        false
    );
    let result = apply(&h, json!({"device_ids":["l1","unknown"],"on":true})).await;
    assert!(result.as_str().unwrap().contains("Часть действий"));
}
#[tokio::test]
async fn blocked_or_invalid_network_never_falls_back_to_direct() {
    let s = server().await;
    let h = home(&s);
    h.request(params("prewarm", Value::Null), json!({}))
        .await
        .unwrap();
    let before = s.state.gets.load(Ordering::Relaxed);
    for network in [
        json!({"error":"VPN недоступен"}),
        json!({"socks":"192.0.2.1:1080","user":"x","pass":"x"}),
        json!({"socks":null}),
        Value::Null,
    ] {
        assert!(
            h.request(
                params(
                    "control",
                    json!({"device_ids":["l1"],"on":true}).to_string().into()
                ),
                network
            )
            .await
            .is_err()
        );
    }
    assert!(s.state.posts.lock().unwrap().is_empty());
    assert_eq!(s.state.gets.load(Ordering::Relaxed), before);
    let mut p = params("context", Value::Null);
    p["alice"] = true.into();
    assert!(
        h.request(p, json!({"error":"blocked"}))
            .await
            .unwrap()
            .as_str()
            .unwrap()
            .contains("alice_command")
    );
}
#[tokio::test]
async fn cache_is_scoped_to_account_and_mutations_invalidate_it() {
    let s = server().await;
    let h = home(&s);
    h.request(params("prewarm", Value::Null), json!({}))
        .await
        .unwrap();
    h.request(params("context", Value::Null), json!({}))
        .await
        .unwrap();
    assert_eq!(s.state.gets.load(Ordering::Relaxed), 1);
    let mut bad = params("settings.validate", Value::Null);
    bad["token"] = "other-account".into();
    assert!(h.request(bad, json!({})).await.is_err());
    h.request(params("prewarm", Value::Null), json!({}))
        .await
        .unwrap();
    let before = s.state.gets.load(Ordering::Relaxed);
    assert_eq!(
        apply(&h, json!({"device_ids":["l1","l1"],"on":false})).await,
        "Готово."
    );
    assert_eq!(
        s.state.posts.lock().unwrap().last().unwrap().1["devices"]
            .as_array()
            .unwrap()
            .len(),
        1
    );
    h.request(params("context", Value::Null), json!({}))
        .await
        .unwrap();
    assert_eq!(s.state.gets.load(Ordering::Relaxed), before + 1);
}
#[test]
fn accepts_null_fields_from_platform_catalog() {
    let c:Catalog=serde_json::from_value(json!({"rooms":null,"groups":null,"devices":[{"id":"x","room":null,"capabilities":null}],"scenarios":null})).unwrap();
    assert!(c.devices[0].room.is_empty());
    assert!(c.groups.is_empty());
}
