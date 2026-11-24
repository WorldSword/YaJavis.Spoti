//! Совместимость прежней панели. Проверка подключения и команды выполняются выбранным модулем.
use super::{App, bad, ok, reply};
use axum::{Json, extract::State, http::StatusCode, response::Response};
use serde_json::{Value, json};
use std::time::Duration;

async fn call(app: &App, op: &str, config: Value, extra: Value) -> Result<Value, String> {
    let mut params = json!({"op":op,"config":config});
    for (k, v) in extra.as_object().into_iter().flatten() {
        params[k] = v.clone();
    }
    app.plugins
        .provider("homeassistant")?
        .request("homeassistant", params, Duration::from_secs(20))
        .await
}
pub async fn get(State(app): State<App>) -> Response {
    let config = app.store.with(|s| s.home_assistant.clone());
    if !crate::settings::has_all(&config, &["url", "token"]) {
        return reply(StatusCode::OK, json!({"configured":false,"entities":[]}));
    }
    match call(&app, "catalog", config, json!({"args":true})).await {
        Ok(entities) => reply(
            StatusCode::OK,
            json!({"configured":true,"entities":entities}),
        ),
        Err(e) => bad(e),
    }
}
pub async fn rooms(State(app): State<App>) -> Response {
    let config = app.store.with(|s| s.home_assistant.clone());
    match call(&app, "rooms", config, Value::Null).await {
        Ok(rooms) => reply(StatusCode::OK, json!({"rooms":rooms})),
        Err(e) => bad(e),
    }
}
fn view(config: &Value) -> Value {
    json!({"url":config["url"],"configured":crate::settings::has_all(config,&["url","token"]),"rooms":config["rooms"]})
}
pub async fn put(State(app): State<App>, Json(config): Json<Value>) -> Response {
    let before = app.store.with(|s| s.home_assistant.clone());
    let validated = match call(
        &app,
        "settings.validate",
        config,
        json!({"previous":before}),
    )
    .await
    {
        Ok(v) => v,
        Err(e) => return bad(e),
    };
    let response = view(&validated);
    match app.store.update(|s| -> Result<(), std::io::Error> {
        if s.home_assistant != before {
            return Err(std::io::Error::other(
                "Настройки подключения изменились; повторите сохранение",
            ));
        }
        let was = s.home_connected("homeassistant");
        s.home_assistant = validated;
        if !was {
            s.home_use.remove("homeassistant");
        }
        Ok(())
    }) {
        Ok(()) => reply(StatusCode::OK, response),
        Err(e) => bad(e.to_string()),
    }
}
pub async fn delete(State(app): State<App>) -> Response {
    match app.store.update(|s| -> Result<(), std::io::Error> {
        s.home_assistant = json!({});
        Ok(())
    }) {
        Ok(()) => ok(),
        Err(e) => bad(e.to_string()),
    }
}
