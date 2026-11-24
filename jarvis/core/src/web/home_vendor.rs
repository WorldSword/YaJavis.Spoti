//! Panel routes shared by the Xiaomi and Smart Life providers.
use super::{App, bad, ok, reply};
use axum::{
    Json,
    body::Bytes,
    extract::State,
    http::{StatusCode, header},
    response::{IntoResponse, Response},
};
use base64::{Engine, engine::general_purpose::STANDARD};
use serde_json::{Value, json};
use std::time::Duration;

async fn call(app: &App, id: &str, op: &str, args: Value, seconds: u64) -> Result<Value, String> {
    app.plugins
        .provider(id)?
        .request(
            id,
            json!({"op":op,"args":args}),
            Duration::from_secs(seconds),
        )
        .await
}
pub async fn state(app: &App, id: &str) -> Value {
    match call(app, id, "state", Value::Null, 1).await {
        Ok(value) if value.is_object() => value,
        _ => json!({"signed_in":app.store.with(|s|s.home_connected(id)),"devices":[]}),
    }
}
async fn login(app: App, id: &str, args: Value) -> Response {
    match call(&app, id, "login", args, 25).await {
        Ok(_) => ok(),
        Err(e) => bad(e),
    }
}
async fn logout(app: App, id: &str) -> Response {
    match call(&app, id, "logout", Value::Null, 10).await {
        Ok(_) => ok(),
        Err(e) => bad(e),
    }
}
async fn refresh(app: App, id: &str) -> Response {
    match call(&app, id, "refresh", Value::Null, 125).await {
        Ok(devices) if devices.is_u64() => reply(StatusCode::OK, json!({"devices":devices})),
        Ok(_) => bad("Неверный ответ списка устройств"),
        Err(e) => bad(e),
    }
}
async fn qr(app: App, id: &str) -> Response {
    let png = call(&app, id, "qr", Value::Null, 5)
        .await
        .ok()
        .and_then(|v| {
            v.as_str()
                .filter(|s| s.len() <= 1_398_104)
                .and_then(|s| STANDARD.decode(s).ok())
        });
    match png {
        Some(png) if png.starts_with(b"\x89PNG\r\n\x1a\n") => (
            [
                (header::CONTENT_TYPE, "image/png"),
                (header::CACHE_CONTROL, "no-store"),
            ],
            Bytes::from(png),
        )
            .into_response(),
        _ => StatusCode::NOT_FOUND.into_response(),
    }
}
pub async fn xiaomi_login(State(app): State<App>) -> Response {
    login(app, "xiaomi", Value::Null).await
}
pub async fn xiaomi_logout(State(app): State<App>) -> Response {
    logout(app, "xiaomi").await
}
pub async fn xiaomi_refresh(State(app): State<App>) -> Response {
    refresh(app, "xiaomi").await
}
pub async fn xiaomi_qr(State(app): State<App>) -> Response {
    qr(app, "xiaomi").await
}
pub async fn smartlife_login(State(app): State<App>, Json(input): Json<Value>) -> Response {
    let Some(code) = input["user_code"]
        .as_str()
        .map(str::trim)
        .filter(|s| !s.is_empty() && s.len() <= 128)
    else {
        return bad("Введите код пользователя Smart Life");
    };
    login(app, "smartlife", json!(code)).await
}
pub async fn smartlife_logout(State(app): State<App>) -> Response {
    logout(app, "smartlife").await
}
pub async fn smartlife_refresh(State(app): State<App>) -> Response {
    refresh(app, "smartlife").await
}
pub async fn smartlife_qr(State(app): State<App>) -> Response {
    qr(app, "smartlife").await
}
