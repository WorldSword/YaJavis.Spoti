//! Анонимный счётчик установок без своего сервера (служебный релиз «stats» на GitHub).
//! Колонка скачивает:
//!   - `new-<модель>`       один раз за всю жизнь (сколько колонок поставили Джарвиса);
//!   - `v<версия>-<модель>` один раз на версию (сколько колонок дошло до этой версии).
//! Файл отметки `/data/.yajavis-counter` общий с Go-версией, чтобы уже учтённые колонки не дублировались.

use crate::settings::Store;
use serde::{Deserialize, Serialize};
use std::{
    path::{Path, PathBuf},
    time::Duration,
};

const MODELS: &[&str] = &[
    "lite",
    "lite2",
    "mini1",
    "mini2",
    "mini3",
    "mini3pro",
    "midi",
    "station2",
    "station3",
    "street",
    "fiero",
];

#[derive(Default, Deserialize, Serialize, PartialEq, Eq, Debug)]
struct State {
    #[serde(default, rename = "new")]
    new_sent: bool,
    #[serde(default)]
    ver: String,
}

pub fn counter_model(id: &str) -> &'static str {
    MODELS.iter().copied().find(|&m| m == id).unwrap_or("other")
}

pub fn counter_path(data_dir: &Path) -> PathBuf {
    if data_dir == Path::new("/data/opt/jarvisd") {
        // Лежит вне /data/opt/jarvisd и совпадает с путём Go-версии:
        // переустановка и переход 0.9.x -> 0.10.x не считают одну колонку дважды.
        PathBuf::from("/data/.yajavis-counter")
    } else {
        data_dir.join("counter.json")
    }
}

async fn ping_url(url: &str) -> Result<bool, String> {
    let mut resp = crate::net::direct()
        .get(url)
        .timeout(Duration::from_secs(30))
        .send()
        .await
        .map_err(|e| crate::net::short(&e))?;
    let code = resp.status().as_u16();
    let mut drained = 0usize;
    while drained < 4096 {
        match resp.chunk().await {
            Ok(Some(chunk)) => drained += chunk.len(),
            Ok(None) | Err(_) => break,
        }
    }
    match code {
        200 => Ok(true),
        404 => Ok(false),
        _ => Err(format!("GitHub ответил {code}")),
    }
}

async fn tick(base_url: &str, path: &Path, model: &str, version: &str) -> bool {
    let mut st = std::fs::read_to_string(path)
        .ok()
        .and_then(|raw| serde_json::from_str::<State>(&raw).ok())
        .unwrap_or_default();
    let mut changed = false;
    let mut failed = false;

    if !st.new_sent {
        match ping_url(&format!("{base_url}/new-{model}")).await {
            Ok(true) => {
                st.new_sent = true;
                changed = true;
            }
            Ok(false) => {}
            Err(e) => {
                log::info!("счётчик установок: {e}");
                failed = true;
            }
        }
    }

    if st.ver != version && !failed {
        match ping_url(&format!("{base_url}/v{version}-{model}")).await {
            Ok(true) => {
                st.ver = version.to_owned();
                changed = true;
            }
            Ok(false) => {}
            Err(e) => {
                log::info!("счётчик установок: {e}");
                failed = true;
            }
        }
    }

    if changed && let Ok(bytes) = serde_json::to_vec(&st) {
        let _ = jarvis_station::atomic(path, &bytes);
    }

    !failed
}

pub async fn run(store: Store, data_dir: PathBuf) {
    let ver = crate::version();
    if ver.is_empty() || ver == "dev" || std::env::var_os("JARVIS_NO_COUNTER").is_some() {
        return;
    }
    tokio::time::sleep(Duration::from_secs(180)).await;
    loop {
        if store.with(|s| s.no_stats) {
            return;
        }
        let repo = store.with(|s| {
            if !s.update_repo.is_empty() && crate::plugins::catalog::valid_repo(&s.update_repo) {
                s.update_repo.clone()
            } else {
                crate::DEFAULT_REPO.to_owned()
            }
        });
        let base_url = format!("https://github.com/{repo}/releases/download/stats");
        let model = counter_model(crate::hardware::station_model().id);
        if tick(&base_url, &counter_path(&data_dir), model, ver).await {
            return;
        }
        tokio::time::sleep(Duration::from_secs(6 * 3600)).await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use tokio::{
        io::{AsyncReadExt, AsyncWriteExt},
        net::TcpListener,
        sync::Mutex,
    };

    struct TempDir(PathBuf);
    impl TempDir {
        fn new() -> Self {
            let p = std::env::temp_dir()
                .join(format!("jarvis-counter-{}", crate::settings::rand_hex(6)));
            std::fs::create_dir_all(&p).unwrap();
            Self(p)
        }
    }
    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    async fn mock_server(status: u16) -> (String, Arc<Mutex<Vec<String>>>) {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let hits = Arc::new(Mutex::new(Vec::new()));
        let hits_clone = hits.clone();
        tokio::spawn(async move {
            while let Ok((mut sock, _)) = listener.accept().await {
                let hits = hits_clone.clone();
                tokio::spawn(async move {
                    let mut buf = [0u8; 2048];
                    let n = sock.read(&mut buf).await.unwrap_or(0);
                    if n == 0 {
                        return;
                    }
                    let req = String::from_utf8_lossy(&buf[..n]);
                    if let Some(path) = req
                        .lines()
                        .next()
                        .and_then(|l| l.split_whitespace().nth(1))
                    {
                        hits.lock().await.push(path.to_owned());
                    }
                    let reason = match status {
                        200 => "OK",
                        404 => "Not Found",
                        _ => "Internal Server Error",
                    };
                    let resp = format!(
                        "HTTP/1.1 {status} {reason}\r\nContent-Length: 3\r\nConnection: close\r\n\r\nok\n"
                    );
                    let _ = sock.write_all(resp.as_bytes()).await;
                    let _ = sock.shutdown().await;
                });
            }
        });
        (format!("http://{addr}"), hits)
    }

    #[test]
    fn maps_known_models_and_unknown_to_other() {
        assert_eq!(counter_model("lite"), "lite");
        assert_eq!(counter_model("mini3pro"), "mini3pro");
        assert_eq!(counter_model("fiero"), "fiero");
        assert_eq!(counter_model("unknown"), "other");
        assert_eq!(counter_model("station1"), "other");
    }

    #[test]
    fn uses_persistent_station_path_and_dev_local_path() {
        assert_eq!(
            counter_path(Path::new("/data/opt/jarvisd")),
            PathBuf::from("/data/.yajavis-counter")
        );
        assert_eq!(
            counter_path(Path::new("/tmp/dev-data")),
            PathBuf::from("/tmp/dev-data/counter.json")
        );
    }

    #[tokio::test]
    async fn preserves_go_state_and_pings_only_new_version_once() {
        let dir = TempDir::new();
        let state_file = dir.0.join("counter.json");
        std::fs::write(&state_file, r#"{"new":true,"ver":"0.9.70"}"#).unwrap();

        let (base, hits) = mock_server(200).await;
        assert!(tick(&base, &state_file, "lite", "0.10.12").await);
        assert_eq!(*hits.lock().await, vec!["/v0.10.12-lite"]);

        let saved: State = serde_json::from_str(&std::fs::read_to_string(&state_file).unwrap()).unwrap();
        assert_eq!(
            saved,
            State {
                new_sent: true,
                ver: "0.10.12".into()
            }
        );

        assert!(tick(&base, &state_file, "lite", "0.10.12").await);
        assert_eq!(hits.lock().await.len(), 1);
    }

    #[tokio::test]
    async fn retries_on_server_error_without_marking_sent() {
        let dir = TempDir::new();
        let state_file = dir.0.join("counter.json");

        let (base, hits) = mock_server(500).await;
        assert!(!tick(&base, &state_file, "mini2", "0.10.12").await);
        assert_eq!(*hits.lock().await, vec!["/new-mini2"]);
        assert!(!state_file.exists());
    }

    #[tokio::test]
    async fn handles_404_without_error_or_marking_sent() {
        let dir = TempDir::new();
        let state_file = dir.0.join("counter.json");

        let (base, hits) = mock_server(404).await;
        assert!(tick(&base, &state_file, "midi", "0.10.12").await);
        assert_eq!(*hits.lock().await, vec!["/new-midi", "/v0.10.12-midi"]);
        assert!(!state_file.exists());
    }
}
