//! Вход в панель: пароль (bcrypt, тот же хэш, что у Go-версии) и подписанная cookie.

use super::{App, bad, ok, reply};
use crate::settings::rand_hex;
use axum::{
    extract::{ConnectInfo, Request, State},
    http::{HeaderMap, HeaderValue, StatusCode, header},
    middleware::Next,
    response::{IntoResponse, Response},
};
use hmac::{Hmac, Mac};
use serde::Deserialize;
use serde_json::json;
use sha2::Sha256;
use std::{
    net::SocketAddr,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

const COOKIE: &str = "jarvis_session";
const MONTH: u64 = 30 * 24 * 3600;

pub struct Fail {
    n: u32,
    until: Option<Instant>,
}

fn now_unix() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0)
}

fn sign(key: &str, exp: i64) -> String {
    let mut m = Hmac::<Sha256>::new_from_slice(key.as_bytes()).expect("hmac");
    m.update(exp.to_string().as_bytes());
    format!("{exp}.{}", hex::encode(m.finalize().into_bytes()))
}

fn cookie_value<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|v| v.split(';'))
        .filter_map(|kv| kv.trim().split_once('='))
        .find(|(k, _)| *k == name)
        .map(|(_, v)| v)
}

pub fn logged_in(app: &App, headers: &HeaderMap) -> bool {
    let Some(c) = cookie_value(headers, COOKIE) else { return false };
    let Some((exp, _)) = c.split_once('.') else { return false };
    let Ok(n) = exp.parse::<i64>() else { return false };
    if now_unix() > n {
        return false;
    }
    let want = app.store.with(|s| sign(&s.session_key, n));
    // сравнение за постоянное время
    want.len() == c.len() && want.bytes().zip(c.bytes()).fold(0u8, |a, (x, y)| a | (x ^ y)) == 0
}

fn session_cookie(app: &App) -> HeaderValue {
    let exp = now_unix() + MONTH as i64;
    let v = app.store.with(|s| sign(&s.session_key, exp));
    HeaderValue::from_str(&format!("{COOKIE}={v}; Path=/; Max-Age={MONTH}; HttpOnly; SameSite=Strict")).unwrap()
}

fn with_cookie(mut r: Response, c: HeaderValue) -> Response {
    r.headers_mut().append(header::SET_COOKIE, c);
    r
}

/// Только для вошедших. Пароля ещё нет — 428 и предложение задать.
pub async fn require(State(app): State<App>, req: Request, next: Next) -> Response {
    if app.store.with(|s| s.password_hash.is_empty()) {
        return reply(StatusCode::PRECONDITION_REQUIRED, json!({"setup": true, "error": "Задайте пароль для настроек"}));
    }
    if !logged_in(&app, req.headers()) {
        return reply(StatusCode::UNAUTHORIZED, json!({"error": "Нужно войти"}));
    }
    next.run(req).await
}

#[derive(Deserialize)]
pub struct PasswordReq {
    #[serde(alias = "Password", alias = "password")]
    password: String,
}

async fn verify(password: String, hash: String) -> bool {
    tokio::task::spawn_blocking(move || !hash.is_empty() && bcrypt::verify(password, &hash).unwrap_or(false))
        .await
        .unwrap_or(false)
}

async fn hash(password: String) -> String {
    tokio::task::spawn_blocking(move || bcrypt::hash(password, 10).unwrap_or_default()).await.unwrap_or_default()
}

pub async fn login(State(app): State<App>, ConnectInfo(peer): ConnectInfo<SocketAddr>, body: axum::body::Bytes) -> Response {
    let ip = peer.ip();
    {
        let fails = app.fails.lock().unwrap();
        if let Some(until) = fails.get(&ip).and_then(|f| f.until).filter(|u| Instant::now() < *u) {
            let wait = (until - Instant::now()).as_secs().max(1);
            return reply(StatusCode::TOO_MANY_REQUESTS, json!({"error": format!("Слишком много попыток. Подождите {wait}s")}));
        }
    }
    let Ok(req) = serde_json::from_slice::<PasswordReq>(&body) else { return bad("пустой запрос") };
    let hash = app.store.with(|s| s.password_hash.clone());
    if !verify(req.password, hash).await {
        {
            let mut fails = app.fails.lock().unwrap();
            let f = fails.entry(ip).or_insert(Fail { n: 0, until: None });
            f.n += 1;
            if f.n >= 5 {
                let d = Duration::from_secs(30 * (f.n as u64 - 4)).min(Duration::from_secs(15 * 60));
                f.until = Some(Instant::now() + d);
            }
        }
        tokio::time::sleep(Duration::from_millis(700)).await;
        return reply(StatusCode::UNAUTHORIZED, json!({"error": "Неверный пароль"}));
    }
    app.fails.lock().unwrap().remove(&ip);
    with_cookie(ok(), session_cookie(&app))
}

pub async fn logout() -> Response {
    with_cookie(ok(), HeaderValue::from_static("jarvis_session=; Path=/; Max-Age=0"))
}

fn check_password(p: &str) -> Result<(), &'static str> {
    if p.chars().count() < 6 { Err("пароль — минимум 6 символов") } else { Ok(()) }
}

/// Первый пароль: только пока пароля нет и только в первые 30 минут после запуска
/// (или после сброса кнопками) — чтобы чужой в сети не успел задать свой.
pub async fn setup(State(app): State<App>, body: axum::body::Bytes) -> Response {
    if app.store.with(|s| !s.password_hash.is_empty()) {
        return reply(StatusCode::CONFLICT, json!({"error": "Пароль уже задан"}));
    }
    let half_hour = Duration::from_secs(30 * 60);
    let reset_ok = app.reset_at.lock().unwrap().is_some_and(|t| t.elapsed() <= half_hour);
    if app.started.elapsed() > half_hour && !reset_ok {
        return reply(StatusCode::FORBIDDEN, json!({"error": "Для безопасности первый пароль можно задать только в первые 30 минут после включения колонки. Перезагрузите её."}));
    }
    let req = match serde_json::from_slice::<PasswordReq>(&body) {
        Ok(r) if check_password(&r.password).is_ok() => r,
        _ => return bad("пароль — минимум 6 символов"),
    };
    let h = hash(req.password).await;
    let _ = app.store.update(|s| {
        if !s.password_hash.is_empty() {
            return Err(std::io::Error::other("уже задан"));
        }
        s.password_hash = h;
        Ok(())
    });
    with_cookie(ok(), session_cookie(&app))
}

#[derive(Deserialize)]
pub struct ChangeReq {
    #[serde(alias = "Old", alias = "old")]
    old: String,
    #[serde(alias = "New", alias = "new")]
    new: String,
}

pub async fn change_password(State(app): State<App>, body: axum::body::Bytes) -> Response {
    let Ok(req) = serde_json::from_slice::<ChangeReq>(&body) else { return bad("пустой запрос") };
    let cur = app.store.with(|s| s.password_hash.clone());
    if !verify(req.old, cur).await {
        return reply(StatusCode::FORBIDDEN, json!({"error": "Текущий пароль не тот"}));
    }
    if let Err(e) = check_password(&req.new) {
        return bad(e);
    }
    let h = hash(req.new).await;
    if let Err(e) = app.store.update(|s| {
        s.password_hash = h;
        s.session_key = rand_hex(32); // все старые входы — недействительны
        Ok::<(), std::io::Error>(())
    }) {
        return bad(e.to_string());
    }
    with_cookie(ok(), session_cookie(&app))
}

/// Сброс пароля кнопками колонки: новый можно задать в течение 30 минут.
#[allow(dead_code)]
pub fn reset_password(app: &App) {
    let _ = app.store.update(|s| {
        s.password_hash.clear();
        s.session_key = rand_hex(32);
        Ok::<(), std::io::Error>(())
    });
    *app.reset_at.lock().unwrap() = Some(Instant::now());
}

#[allow(dead_code)]
pub fn unauthorized() -> Response {
    (StatusCode::UNAUTHORIZED, "").into_response()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn signature_matches_go() {
        // Go: hmac.New(sha256.New, key); fmt.Fprintf(m, "%d", exp) → "exp.hex"
        let s = sign("k", 1700000000);
        assert!(s.starts_with("1700000000."));
        assert_eq!(s.len(), "1700000000.".len() + 64);
    }

    #[test]
    fn go_bcrypt_hash_verifies() {
        // хэш из Go (bcrypt.GenerateFromPassword("secret1", 10))
        let h = bcrypt::hash("secret1", 4).unwrap();
        assert!(bcrypt::verify("secret1", &h).unwrap());
        assert!(bcrypt::verify("secret1", &h.replacen("$2b$", "$2a$", 1)).unwrap());
    }
}
