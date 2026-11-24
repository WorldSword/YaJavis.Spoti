use super::{App, bad, ok, reply};
use crate::{training_io, wake_profiles};
use base64::{Engine, engine::general_purpose::STANDARD as B64};
use axum::{
    Json,
    body::Bytes,
    extract::{Path, Query, Request, State},
    http::StatusCode,
    middleware::Next,
    response::Response,
};
use serde::Deserialize;
use serde_json::json;

pub const DISABLED: &str = "Включите плагин «Обучение слова»";

pub async fn require_plugin(State(app): State<App>, request: Request, next: Next) -> Response {
    if !app.plugins.wake_training() {
        return reply(StatusCode::FORBIDDEN, json!({"error": DISABLED}));
    }
    next.run(request).await
}

#[derive(Deserialize)]
pub struct NamedModel {
    phrase: String,
}

#[derive(Deserialize)]
pub struct Selection {
    id: String,
}

pub async fn list(State(app): State<App>) -> Response {
    let path = app.data_dir.clone();
    let list = tokio::task::spawn_blocking(move || wake_profiles::list(&path)).await;
    match list {
        Ok(profiles) => reply(
            StatusCode::OK,
            json!({
                "profiles":profiles, "selected":app.store.with(|s| s.wake_profile.clone()),
                "phrase":app.engine.listener.phrase(),
                "training":true
            }),
        ),
        Err(_) => bad("Не удалось прочитать профили слова"),
    }
}

pub async fn upload(
    State(app): State<App>,
    Query(input): Query<NamedModel>,
    body: Bytes,
) -> Response {
    let guard = app.wake_update.clone().lock_owned().await;
    let path = app.data_dir.clone();
    let store = app.store.clone();
    let plugins = app.plugins.clone();
    let result = tokio::task::spawn_blocking(move || {
        let _guard = guard;
        if !plugins.wake_training() { return Err(DISABLED.into()); }
        wake_profiles::save(&path, &input.phrase, &body, &store)
    })
    .await;
    respond_profile(result)
}

fn respond_profile(
    result: Result<Result<wake_profiles::Profile, String>, tokio::task::JoinError>,
) -> Response {
    match result {
        Ok(Ok(profile)) => reply(StatusCode::OK, json!({"ok":true,"profile":profile})),
        Ok(Err(error)) => bad(error),
        Err(_) => bad("Не удалось сохранить фразу"),
    }
}

pub async fn select(State(app): State<App>, Json(input): Json<Selection>) -> Response {
    let guard = app.wake_update.clone().lock_owned().await;
    let path = app.data_dir.clone();
    let store = app.store.clone();
    let listener = app.engine.listener.clone();
    let plugins = app.plugins.clone();
    let result = tokio::task::spawn_blocking(move || {
        let _guard = guard;
        if !plugins.wake_training() { return Err(DISABLED.into()); }
        wake_profiles::select(&path, &input.id, &store)?;
        listener.request_reload();
        Ok::<(), String>(())
    })
    .await;
    match result {
        Ok(Ok(())) => ok(),
        Ok(Err(error)) => bad(error),
        Err(_) => bad("Не удалось включить модель"),
    }
}

pub async fn remove(State(app): State<App>, Path(id): Path<String>) -> Response {
    let guard = app.wake_update.clone().lock_owned().await;
    let path = app.data_dir.clone();
    let store = app.store.clone();
    let plugins = app.plugins.clone();
    let result = tokio::task::spawn_blocking(move || {
        let _guard = guard;
        if !plugins.wake_training() { return Err(DISABLED.into()); }
        wake_profiles::remove(&path, &id, &store)
    })
    .await;
    match result {
        Ok(Ok(())) => ok(),
        Ok(Err(error)) => bad(error),
        Err(_) => bad("Не удалось удалить модель"),
    }
}

pub async fn capture(State(app): State<App>) -> Response {
    use training_io::SAMPLES;
    let Ok(guard) = app.wake_update.clone().try_lock_owned() else {
        return bad("Идёт другая запись или обучение");
    };
    if !app.engine.mic.online() {
        return bad("Микрофон колонки недоступен");
    }
    let mic = app.engine.mic.clone();
    let engine = app.engine.clone();
    let result = tokio::task::spawn_blocking(move || {
        let _guard = guard;
        let _recording = engine.recording_word();
        let (id, rx) = mic.subscribe(32);
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(6);
        let mut samples = Vec::with_capacity(SAMPLES * 2);
        let result = loop {
            if !engine.host.wake_training() {
                break Err(DISABLED);
            }
            if engine.talking() || engine.player.playing() || engine.music.playing() {
                break Err("Остановите разговор или музыку и запишите ещё раз");
            }
            let Some(left) = deadline.checked_duration_since(std::time::Instant::now()) else {
                break Err("Запись не завершилась");
            };
            let Ok(frame) = rx.recv_timeout(left) else {
                break Err("Микрофон не передал звук");
            };
            let n = frame.len().min(SAMPLES - samples.len() / 2);
            samples.extend(frame[..n].iter().flat_map(|s| s.to_le_bytes()));
            if samples.len() == SAMPLES * 2 {
                break Ok(samples);
            }
        };
        mic.unsubscribe(id);
        result
    })
    .await;
    match result {
        Ok(Ok(samples)) => Response::builder()
            .header("Content-Type", "application/octet-stream")
            .header("Cache-Control", "no-store")
            .body(axum::body::Body::from(samples))
            .unwrap(),
        Ok(Err(error)) => bad(error),
        Err(_) => bad("Запись не завершилась"),
    }
}

pub async fn train(
    State(app): State<App>,
    Query(input): Query<NamedModel>,
    body: Bytes,
) -> Response {
    let Ok(guard) = app.wake_update.clone().try_lock_owned() else {
        return bad("Идёт другая запись или обучение");
    };
    let phrase = match wake_profiles::phrase(&input.phrase) {
        Ok(value) => value,
        Err(error) => return bad(error),
    };
    if body.len() != training_io::BODY_BYTES {
        return bad("Нужны 10 записей фразы и 3 записи фона");
    }
    let transfer = match training_io::Transfer::new() {
        Ok(transfer) => transfer,
        Err(error) => return bad(error),
    };
    if let Err(error) = tokio::fs::write(transfer.path(), &body).await {
        return bad(error.to_string());
    }
    let trained = training_io::call(&app.plugins, "training.train", json!({"file":transfer.path()}), std::time::Duration::from_secs(180)).await;
    let head = match trained.and_then(|value| B64.decode(value.as_str().unwrap_or_default()).map_err(|error| error.to_string())) {
        Ok(head) => head,
        Err(error) => return bad(error),
    };
    let data = app.data_dir.clone();
    let store = app.store.clone();
    let plugins = app.plugins.clone();
    let result = tokio::task::spawn_blocking(move || {
        let _guard = guard;
        if !plugins.wake_training() { return Err(DISABLED.into()); }
        wake_profiles::save(&data, &phrase, &head, &store)
    })
    .await;
    respond_profile(result)
}

pub async fn test(State(app): State<App>, Path(id): Path<String>, body: Bytes) -> Response {
    if body.len() != training_io::SAMPLES * 2 {
        return bad("Нужна одна запись фразы");
    }
    let Ok(guard) = app.wake_update.clone().try_lock_owned() else {
        return bad("Идёт другая запись или обучение");
    };
    let data = app.data_dir.clone();
    let store = app.store.clone();
    let result = tokio::task::spawn_blocking(move || -> Result<(bool, f32), String> {
        let _guard = guard;
        let (_, bytes) = wake_profiles::get(&data, &id)?;
        let mut detector = wake_profiles::model(&data, &bytes, &store)?;
        let mut signal = vec![0; 32000];
        signal.extend(
            body.chunks_exact(2)
                .map(|b| i16::from_le_bytes([b[0], b[1]])),
        );
        signal.resize(signal.len() + 6400, 0);
        let mut hit = false;
        let mut peak = 0.0f32;
        for frame in signal.chunks(160) {
            hit |= detector.feed(frame);
            peak = peak.max(detector.last_prob());
        }
        Ok((hit, peak))
    })
    .await;
    match result {
        Ok(Ok((hit, peak))) => reply(StatusCode::OK, json!({"detected":hit,"peak":peak})),
        Ok(Err(error)) => bad(error),
        Err(_) => bad("Не удалось проверить фразу"),
    }
}
