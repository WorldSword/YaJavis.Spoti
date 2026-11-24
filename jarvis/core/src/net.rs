//! Интернет для ядра. Напрямую — для GitHub (обновления, каталог); через VPN — по галочке подключения (vpn.rs).

use serde_json::Value;
use std::{sync::OnceLock, time::Duration};

pub fn user_agent() -> String {
    format!("jarvisd/{}", crate::version())
}

/// Прямой клиент (без VPN): GitHub, каталог плагинов, обновления.
pub fn direct() -> &'static reqwest::Client {
    static C: OnceLock<reqwest::Client> = OnceLock::new();
    C.get_or_init(|| {
        reqwest::Client::builder()
            .user_agent(user_agent())
            .connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(60))
            .pool_max_idle_per_host(2)
            .build()
            .expect("http")
    })
}

/// Клиент через SOCKS5 туннеля VPN (socks — «127.0.0.1:port»).
pub fn via_socks(socks: &str, user: &str, pass: &str, timeout: Duration) -> Result<reqwest::Client, String> {
    let proxy = reqwest::Proxy::all(format!("socks5h://{socks}")).map_err(|e| e.to_string())?.basic_auth(user, pass);
    reqwest::Client::builder()
        .user_agent(user_agent())
        .proxy(proxy)
        .connect_timeout(Duration::from_secs(10))
        .timeout(timeout)
        .build()
        .map_err(|e| e.to_string())
}

pub async fn get_json(url: &str) -> Result<Value, String> {
    let resp = direct()
        .get(url)
        .header("Accept", "application/vnd.github+json")
        .timeout(Duration::from_secs(25))
        .send()
        .await
        .map_err(|e| short(&e))?;
    let code = resp.status().as_u16();
    if code != 200 {
        return Err(format!("GitHub ответил {code}"));
    }
    let b = resp.bytes().await.map_err(|e| short(&e))?;
    if b.len() > 4 << 20 {
        return Err("слишком большой ответ".into());
    }
    serde_json::from_slice(&b).map_err(|e| e.to_string())
}

pub async fn download(url: &str, max: usize) -> Result<Vec<u8>, String> {
    let mut resp = direct().get(url).timeout(Duration::from_secs(300)).send().await.map_err(|e| short(&e))?;
    let code = resp.status().as_u16();
    if code != 200 {
        return Err(format!("скачивание: ответ {code}"));
    }
    let mut out = Vec::new();
    while let Some(chunk) = resp.chunk().await.map_err(|e| short(&e))? {
        out.extend_from_slice(&chunk);
        if out.len() > max {
            return Err(format!("файл больше {} МБ", max >> 20));
        }
    }
    Ok(out)
}

/// Короткая причина ошибки сети — для панели и журнала.
pub fn short(e: &reqwest::Error) -> String {
    if e.is_timeout() {
        return "нет ответа (таймаут)".into();
    }
    if e.is_connect() {
        return "нет соединения".into();
    }
    let mut s = e.to_string();
    let mut src = std::error::Error::source(e);
    while let Some(x) = src {
        s = x.to_string();
        src = x.source();
    }
    s
}
