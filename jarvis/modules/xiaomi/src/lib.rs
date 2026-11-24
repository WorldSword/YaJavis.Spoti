//! Xiaomi Home account and MIoT device operations.
mod cloud;
mod devices;
mod login;

use jarvis_home_common::{Host, load, random, save};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    path::PathBuf,
    sync::{Arc, Mutex},
    time::Duration,
};

const CACHE: &str = "xiaomi-devices.json";

#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(default)]
struct Auth {
    user_id: String,
    cuser_id: String,
    ssecurity: String,
    pass_token: String,
    service_token: String,
    region: String,
}
impl Auth {
    fn signed_in(&self) -> bool {
        !self.user_id.is_empty() && !self.ssecurity.is_empty() && !self.service_token.is_empty()
    }
}

#[derive(Default)]
struct Login {
    png: String,
    url: String,
    error: String,
    at: i64,
    busy: bool,
}
pub struct Xiaomi {
    host: Host,
    agent: String,
    account: tokio::sync::Mutex<()>,
    catalog: tokio::sync::Mutex<()>,
    devices: Mutex<Option<Vec<devices::Device>>>,
    login: Mutex<Login>,
    poller: Mutex<Option<tokio::task::JoinHandle<()>>>,
}
impl Xiaomi {
    pub fn new(host: Host) -> Result<Self, String> {
        let bytes = random::<31>()?;
        let lower: String = bytes[..18]
            .iter()
            .map(|b| (b'a' + b % 26) as char)
            .collect();
        let upper: String = bytes[18..].iter().map(|b| (b'A' + b % 5) as char).collect();
        Ok(Self {
            host,
            agent: format!("{lower}-{upper} APP/com.xiaomi.mihome APPV/10.5.201"),
            account: Default::default(),
            catalog: Default::default(),
            devices: Default::default(),
            login: Default::default(),
            poller: Default::default(),
        })
    }
    async fn auth(&self) -> Result<Auth, String> {
        let cfg = self.host.config().await?;
        let auth = cfg
            .get("auth")
            .filter(|v| v.is_object())
            .cloned()
            .unwrap_or_else(|| json!({}));
        serde_json::from_value(auth).map_err(|_| "Неверные настройки Xiaomi".into())
    }
    async fn update(&self, before: &Auth, after: &Auth, connected: bool) -> Result<(), String> {
        self.host.set(json!(before), json!(after), connected).await
    }
    fn cache(&self) -> PathBuf {
        self.host.data.join(CACHE)
    }
    fn devices(&self) -> Vec<devices::Device> {
        self.devices
            .lock()
            .unwrap()
            .get_or_insert_with(|| {
                load(&self.cache())
                    .ok()
                    .and_then(|v| serde_json::from_value(v).ok())
                    .unwrap_or_default()
            })
            .clone()
    }
    fn clear_catalog(&self) -> Result<(), String> {
        *self.devices.lock().unwrap() = Some(vec![]);
        save(&self.cache(), b"[]")
    }
    fn cancel_login(&self) {
        if let Some(task) = self.poller.lock().unwrap().take() {
            task.abort();
        }
    }
    pub fn stop(&self) {
        self.cancel_login();
        self.host.close();
    }
    fn login_state(&self) -> Value {
        let login = self.login.lock().unwrap();
        json!({"code":!login.png.is_empty(),"url":login.url,"error":login.error,"at":login.at,"busy":login.busy})
    }
    pub async fn request(self: &Arc<Self>, params: Value) -> Result<Value, String> {
        if matches!(
            params["op"].as_str(),
            Some("state" | "devices" | "context" | "control" | "refresh")
        ) {
            self.host.migrate(CACHE).await?;
        }
        match params["op"].as_str().unwrap_or("") {
            "login" => {
                self.begin_login().await?;
                Ok(true.into())
            }
            "logout" => {
                let _gate = self.account.lock().await;
                self.cancel_login();
                let auth = self.auth().await?;
                self.update(&auth, &Auth::default(), false).await?;
                *self.login.lock().unwrap() = Login::default();
                self.clear_catalog()?;
                Ok(true.into())
            }
            "login-state" => Ok(self.login_state()),
            "qr" => Ok(self.login.lock().unwrap().png.clone().into()),
            "refresh" => Ok(
                tokio::time::timeout(Duration::from_secs(120), self.refresh())
                    .await
                    .map_err(|_| "Xiaomi долго обновляет устройства")??
                    .into(),
            ),
            op => {
                let auth = self.auth().await?;
                let devices = if auth.signed_in() {
                    self.devices()
                } else {
                    vec![]
                };
                match op {
                    "state" => Ok(
                        json!({"signed_in":auth.signed_in(),"region":auth.region,"login":self.login_state(),"devices":devices.iter().map(|d|d.view()).collect::<Vec<_>>()}),
                    ),
                    "devices" => Ok(json!(devices)),
                    "context" => Ok(devices::context(&devices).into()),
                    "control" => Ok(self
                        .control(&auth, &devices, &params["args"])
                        .await
                        .unwrap_or_else(|e| format!("Xiaomi: {e}"))
                        .into()),
                    _ => Err("Неизвестная команда Xiaomi".into()),
                }
            }
        }
    }
}
impl Drop for Xiaomi {
    fn drop(&mut self) {
        self.stop();
    }
}
