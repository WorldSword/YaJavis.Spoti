use super::{Host, Manifest, valid_tool_name};
use jarvis_native_plugins::{Context, Plugin, Registration};
use serde_json::{Map, Value, json};
use std::{path::PathBuf, sync::Arc, time::Duration};
use tokio::sync::watch;

pub fn entries() -> impl Iterator<Item = (Manifest, &'static Registration)> {
    jarvis_native_plugins::PLUGINS.iter().filter_map(|entry| {
        let manifest: Manifest = serde_json::from_str(entry.manifest).ok()?;
        manifest.validate().ok()?;
        (manifest.kind == "module" && !super::modules::built_in(&manifest.id))
            .then_some((manifest, entry))
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn rust_plugin_installs_runs_and_removes_its_files() {
        if !entries().any(|(manifest, _)| manifest.id == "native-echo") {
            return;
        }
        let dir =
            std::env::temp_dir().join(format!("jarvis-native-{}", crate::settings::rand_hex(6)));
        let host = Host::new(dir.clone(), super::super::Hooks::default());
        host.install_module("native-echo").await.unwrap();
        assert_eq!(host.text_plugins()[0]["id"], "native-echo");
        let reply = host
            .plugin_chat(
                "native-echo",
                json!({"messages":[{"role":"user","content":"echo"}]}),
            )
            .await;
        assert_eq!(reply.unwrap(), "Rust: echo");
        assert_eq!(
            host.command("Rust echo").await.as_deref(),
            Some("Rust Echo")
        );
        host.uninstall("native-echo").await.unwrap();
        assert!(!host.dir().join("native-echo").exists());
        assert!(host.text_plugins().is_empty());
        std::fs::remove_dir_all(dir).unwrap();
    }
}

pub struct Native {
    plugin: Box<dyn Plugin>,
    chat: bool,
    stt: bool,
    tts: bool,
    stopped: watch::Sender<bool>,
}

impl Native {
    pub fn start(
        entry: &Registration,
        manifest: &Manifest,
        host: &Arc<Host>,
        id: &str,
        dir: PathBuf,
        settings: Map<String, Value>,
    ) -> Result<Self, String> {
        let info = Arc::downgrade(host);
        let say = Arc::downgrade(host);
        let services = Arc::downgrade(host);
        let id = id.to_owned();
        let data_dir = dir.join("data");
        std::fs::create_dir_all(&data_dir).map_err(|error| error.to_string())?;
        let context = Context {
            data_dir,
            settings,
            info: Arc::new(move || {
                info.upgrade()
                    .map(|host| (host.hooks.info)())
                    .unwrap_or_default()
            }),
            say: Arc::new(move |text| {
                if let Some(host) = say.upgrade() {
                    (host.hooks.say)(text);
                }
            }),
            service: Arc::new(move |method, params| {
                let host = services.clone();
                let id = id.clone();
                Box::pin(async move {
                    let host = host.upgrade().ok_or("Ядро завершилось")?;
                    (host.hooks.request)(id, method, params).await
                })
            }),
        };
        Ok(Self {
            plugin: (entry.start)(context)?,
            chat: entry.chat,
            stt: manifest.provides("stt"),
            tts: manifest.provides("tts"),
            stopped: watch::channel(false).0,
        })
    }

    pub fn supports_chat(&self) -> bool {
        self.chat && !*self.stopped.borrow()
    }

    pub fn supports_stt(&self) -> bool {
        self.stt && !*self.stopped.borrow()
    }

    pub fn supports_tts(&self) -> bool {
        self.tts && !*self.stopped.borrow()
    }

    pub async fn request(
        &self,
        method: &str,
        args: Value,
        timeout: Duration,
    ) -> Result<Value, String> {
        let mut stopped = self.stopped.subscribe();
        if *stopped.borrow() {
            return Err("Модуль отключён".into());
        }
        tokio::select! {
            _ = stopped.changed() => Err("Модуль отключён".into()),
            result = tokio::time::timeout(timeout, self.plugin.request(method, args)) =>
                result.map_err(|_| "Модуль не ответил вовремя".to_string())?,
        }
    }

    pub fn tools(&self) -> Vec<Value> {
        if *self.stopped.borrow() {
            return vec![];
        }
        self.plugin
            .tools()
            .into_iter()
            .filter(|tool| {
                tool["type"] == "function"
                    && tool["name"].as_str().is_some_and(valid_tool_name)
                    && tool["parameters"].is_object()
            })
            .take(64)
            .collect()
    }

    pub async fn call_tool(&self, name: &str, args: &str) -> Option<String> {
        if !self.tools().iter().any(|tool| tool["name"] == name) {
            return None;
        }
        let arguments = match serde_json::from_str::<Value>(args) {
            Ok(value) => value,
            Err(_) => return Some("Неверные аргументы инструмента".into()),
        };
        Some(
            match self
                .request(
                    "tool",
                    json!({"name": name, "arguments": arguments}),
                    Duration::from_secs(15),
                )
                .await
            {
                Ok(Value::String(answer)) => answer,
                Ok(value) => value.to_string(),
                Err(error) => error,
            },
        )
    }

    pub fn configure(&self, settings: Map<String, Value>) {
        if !*self.stopped.borrow() {
            self.plugin.configure(settings);
        }
    }

    pub fn stop(&self) {
        if !self.stopped.send_replace(true) {
            self.plugin.stop();
        }
    }
}
