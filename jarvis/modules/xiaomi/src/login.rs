use super::{Auth, Login, Xiaomi};
use base64::{Engine, engine::general_purpose::STANDARD as B64};
use jarvis_home_common::{bytes, now, text};
use reqwest::{Client, Url, header};
use serde_json::Value;
use std::{sync::Arc, time::Duration};

const ACCOUNT: &str = "https://account.xiaomi.com";
fn url(raw: &str) -> Result<Url, String> {
    let url = Url::parse(raw).map_err(|_| "Неверный адрес входа Xiaomi")?;
    let host = url.host_str().unwrap_or("");
    let allowed = ["xiaomi.com", "mi.com", "xiaomi.net"];
    if url.scheme() != "https"
        || url.port().is_some()
        || !url.username().is_empty()
        || url.password().is_some()
        || !allowed
            .iter()
            .any(|d| host == *d || host.strip_suffix(d).is_some_and(|p| p.ends_with('.')))
    {
        return Err("Неверный адрес входа Xiaomi".into());
    }
    Ok(url)
}
async fn decode(response: reqwest::Response) -> Result<Value, String> {
    let body = bytes(response, 1 << 20).await?;
    let body = std::str::from_utf8(&body)
        .map_err(|_| "Неверный ответ входа Xiaomi")?
        .trim();
    serde_json::from_str(body.strip_prefix("&&&START&&&").unwrap_or(body))
        .map_err(|_| "Неверный ответ входа Xiaomi".into())
}

impl Xiaomi {
    async fn fetch(&self, client: &Client, url: Url) -> Result<reqwest::Response, String> {
        client
            .get(url)
            .header("User-Agent", &self.agent)
            .send()
            .await
            .map_err(|_| "Xiaomi недоступен".into())
    }
    async fn service_token(&self, client: &Client, location: &str) -> Result<String, String> {
        let mut location = url(location)?;
        for _ in 0..8 {
            let response = self.fetch(client, location.clone()).await?;
            if let Some(token) = response
                .cookies()
                .find(|c| c.name() == "serviceToken" && !c.value().is_empty())
            {
                return Ok(token.value().into());
            }
            if !response.status().is_redirection() {
                break;
            }
            let next = response
                .headers()
                .get(header::LOCATION)
                .and_then(|v| v.to_str().ok())
                .ok_or("Нет адреса входа Xiaomi")?;
            location = url(location
                .join(next)
                .map_err(|_| "Неверный переход Xiaomi")?
                .as_str())?;
        }
        Err("Xiaomi не выдал serviceToken".into())
    }
    pub(super) async fn renew(&self, before: &Auth) -> Result<Auth, String> {
        if before.pass_token.is_empty() {
            return Err("Войдите в Xiaomi Home по QR-коду".into());
        }
        let client = self.host.session().await?;
        let response = client
            .get(format!(
                "{ACCOUNT}/pass/serviceLogin?sid=xiaomiio&_json=true"
            ))
            .header("User-Agent", &self.agent)
            .header(
                "Cookie",
                format!(
                    "userId={}; cUserId={}; passToken={}",
                    before.user_id, before.cuser_id, before.pass_token
                ),
            )
            .send()
            .await
            .map_err(|_| "Xiaomi недоступен")?;
        let value = decode(response).await?;
        let mut after = before.clone();
        after.ssecurity = value["ssecurity"]
            .as_str()
            .filter(|s| !s.is_empty())
            .ok_or("Войдите в Xiaomi Home по QR-коду")?
            .into();
        after.service_token = self
            .service_token(&client, value["location"].as_str().unwrap_or(""))
            .await?;
        if let Some(pass) = value["passToken"].as_str().filter(|s| !s.is_empty()) {
            after.pass_token = pass.into();
        }
        self.update(before, &after, false).await?;
        Ok(after)
    }
    pub async fn begin_login(self: &Arc<Self>) -> Result<(), String> {
        let _gate = self.account.lock().await;
        self.cancel_login();
        *self.login.lock().unwrap() = Login::default();
        let previous = self.auth().await?;
        let client = self.host.session().await?;
        let start = client
            .get(format!("{ACCOUNT}/longPolling/loginUrl"))
            .query(&[
                ("_qrsize", "360"),
                ("qs", "%3Fsid%3Dxiaomiio%26_json%3Dtrue"),
                ("callback", "https://sts.api.io.mi.com/sts"),
                ("_hasLogo", "false"),
                ("sid", "xiaomiio"),
                ("serviceParam", ""),
                ("_locale", "ru_RU"),
                ("_dc", &now().to_string()),
            ])
            .header("User-Agent", &self.agent)
            .send()
            .await
            .map_err(|_| "Xiaomi недоступен")?;
        let start = decode(start).await?;
        let poll = url(start["lp"].as_str().unwrap_or(""))?;
        let image = bytes(
            self.fetch(&client, url(start["qr"].as_str().unwrap_or(""))?)
                .await?,
            1 << 20,
        )
        .await?;
        if !image.starts_with(b"\x89PNG\r\n\x1a\n") {
            return Err("Xiaomi не вернул PNG с QR-кодом".into());
        }
        let login_url = url(start["loginUrl"].as_str().unwrap_or(""))
            .map(|u| u.to_string())
            .unwrap_or_default();
        *self.login.lock().unwrap() = Login {
            png: B64.encode(image),
            url: login_url,
            at: now(),
            busy: true,
            ..Default::default()
        };
        let timeout = Duration::from_secs(start["timeout"].as_u64().unwrap_or(120).clamp(120, 300));
        let this = self.clone();
        let task = tokio::spawn(async move {
            let result = tokio::time::timeout(timeout, async {
                loop {
                    tokio::time::sleep(Duration::from_secs(1)).await;
                    let Ok(response) = this.fetch(&client, poll.clone()).await else {
                        continue;
                    };
                    let Ok(value) = decode(response).await else {
                        continue;
                    };
                    let Some(security) = value["ssecurity"].as_str().filter(|s| !s.is_empty())
                    else {
                        continue;
                    };
                    let token = this
                        .service_token(&client, value["location"].as_str().unwrap_or(""))
                        .await?;
                    let auth = Auth {
                        user_id: text(&value["userId"]),
                        cuser_id: text(&value["cUserId"]),
                        ssecurity: security.into(),
                        pass_token: text(&value["passToken"]),
                        service_token: token,
                        region: String::new(),
                    };
                    if !auth.signed_in() {
                        return Err("Xiaomi не выдал данные входа".into());
                    }
                    let _gate = this.account.lock().await;
                    this.update(&previous, &auth, true).await?;
                    this.clear_catalog()?;
                    return Ok::<(), String>(());
                }
            })
            .await;
            let error = match result {
                Ok(Ok(())) => String::new(),
                Ok(Err(e)) => e,
                Err(_) => "QR-код устарел. Нажмите «Войти» ещё раз".into(),
            };
            let ok = error.is_empty();
            *this.login.lock().unwrap() = Login {
                error,
                ..Default::default()
            };
            if ok {
                let _ = this.refresh().await;
            }
        });
        *self.poller.lock().unwrap() = Some(task);
        Ok(())
    }
}
