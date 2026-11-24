//! Страница «Память»: досье, конспект, летопись и архив реплик — посмотреть, поправить, удалить.
//! Любая правка отменяет сжатие, которое шло в этот момент (оно повторится само).

use super::{App, bad, reply};
use crate::memory::{Memory, clip};
use axum::{
    body::Bytes,
    extract::{Query, State},
    http::StatusCode,
    response::Response,
};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{collections::HashMap, sync::Arc};

fn mem(app: &App) -> Option<Arc<Memory>> {
    app.engine.tools.memory.clone()
}

fn body<T: for<'de> Deserialize<'de>>(b: &Bytes) -> Option<T> {
    serde_json::from_slice(b).ok()
}

pub async fn get(State(app): State<App>) -> Response {
    let Some(m) = mem(&app) else { return bad("память выключена в этой сборке") };
    let mut st = m.state();
    if let Some(o) = st.as_object_mut() {
        o.remove("recent"); // реплики страница листает сама — через архив
    }
    reply(StatusCode::OK, json!({"memory": st, "settings": super::state::memory_state(&app.store.get(), &app.plugins)}))
}

pub async fn put(State(app): State<App>, b: Bytes) -> Response {
    #[derive(Deserialize)]
    struct Req {
        profile: Option<String>,
        summary: Option<String>,
    }
    let (Some(m), Some(req)) = (mem(&app), body::<Req>(&b)) else { return bad("пустой запрос") };
    if let Some(p) = req.profile {
        if let Err(e) = m.set_profile(&p) {
            return bad(e);
        }
        log::info!("память: досье изменено в панели");
    }
    if let Some(s) = req.summary {
        if let Err(e) = m.set_summary(&s) {
            return bad(e);
        }
        log::info!("память: конспект изменён в панели");
    }
    get(State(app)).await
}

pub async fn month(State(app): State<App>, b: Bytes) -> Response {
    #[derive(Deserialize)]
    struct Req {
        #[serde(default)]
        month: String,
        #[serde(default)]
        text: String,
    }
    let (Some(m), Some(req)) = (mem(&app), body::<Req>(&b)) else { return bad("пустой запрос") };
    if req.month.is_empty() {
        return bad("пустой запрос");
    }
    if let Err(e) = m.set_month(&req.month, &req.text) {
        return bad(e);
    }
    log::info!("память: летопись за {} изменена в панели", req.month);
    get(State(app)).await
}

pub async fn archive(State(app): State<App>, Query(q): Query<HashMap<String, String>>) -> Response {
    let Some(m) = mem(&app) else { return reply(StatusCode::OK, json!({"turns": []})) };
    let num = |k: &str| q.get(k).and_then(|v| v.parse::<i64>().ok()).unwrap_or(0);
    let (turns, more) = m.archive_page(&clip(q.get("q").map_or("", String::as_str), 200), num("before"), num("limit").max(0) as usize);
    reply(StatusCode::OK, json!({"turns": turns, "more": more}))
}

pub async fn forget(State(app): State<App>, b: Bytes) -> Response {
    #[derive(Deserialize)]
    struct Req {
        #[serde(default)]
        n: Vec<i64>,
    }
    let (Some(m), Some(req)) = (mem(&app), body::<Req>(&b)) else { return bad("какие реплики удалить?") };
    if req.n.is_empty() || req.n.len() > 500 {
        return bad("какие реплики удалить?");
    }
    let n = m.delete_turns(&req.n);
    log::info!("память: из архива удалено реплик в панели: {n}");
    reply(StatusCode::OK, json!({"ok": true, "deleted": n}))
}

/// «Обновить досье сейчас»: сжать реплики в досье и конспект, не дожидаясь длинного разговора.
pub async fn compact(State(app): State<App>) -> Response {
    let Some(m) = mem(&app) else { return bad("память выключена в этой сборке") };
    match tokio::time::timeout(std::time::Duration::from_secs(180), m.compact_now()).await {
        Ok(Ok(n)) => reply(StatusCode::OK, json!({"ok": true, "turns": n})),
        Ok(Err(e)) => bad(clip(&e, 300)),
        Err(_) => bad("модель не ответила за 3 минуты"),
    }
}

/// «Начать новый чат» (конспект и недавние реплики забыть, архив остаётся); ?all=1 — стереть всю память.
pub async fn new_chat(State(app): State<App>, Query(q): Query<HashMap<String, String>>) -> Response {
    let Some(m) = mem(&app) else { return bad("память выключена в этой сборке") };
    if q.get("all").map(String::as_str) == Some("1") {
        m.erase();
        log::info!("память: стёрта целиком из панели");
    } else {
        m.reset();
    }
    reply(StatusCode::OK, json!({"ok": true}))
}

/// Что записано — для общего состояния панели («chat»).
pub fn chat_state(app: &App) -> Value {
    mem(app).map_or(Value::Null, |m| m.state())
}
