//! One audio buffer and one upload, with no background audio written to flash.
use crate::{
    storage as net,
    storage::{rand_hex, write_atomic},
    training_records::pcm_wav,
};
use reqwest::{Method, StatusCode, Url};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    fs,
    io::{Cursor, Write},
    path::PathBuf,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

const RATE: usize = 16_000;
const MINUTE: usize = 60 * RATE;

#[derive(Deserialize)]
#[serde(tag = "provider", rename_all = "snake_case")]
pub enum Login {
    Nextcloud {
        link: String,
        #[serde(default)]
        password: String,
    },
    Koofr {
        email: String,
        password: String,
    },
}

#[derive(Serialize, Deserialize, Clone)]
struct Destination {
    provider: String,
    endpoint: String,
    user: String,
    password: String,
}

fn client() -> &'static reqwest::Client {
    static CLIENT: std::sync::OnceLock<reqwest::Client> = std::sync::OnceLock::new();
    CLIENT.get_or_init(|| {
        reqwest::Client::builder()
            .user_agent(net::user_agent())
            .redirect(reqwest::redirect::Policy::none())
            .connect_timeout(Duration::from_secs(8))
            .timeout(Duration::from_secs(25))
            .build()
            .expect("клиент WebDAV")
    })
}

impl Destination {
    fn from_login(login: Login) -> Result<Self, String> {
        match login {
            Login::Nextcloud { link, password } => {
                let mut url = Url::parse(link.trim())
                    .map_err(|_| "Вставьте ссылку Nextcloud на папку «Только загрузка»")?;
                if url.scheme() != "https"
                    || url.host_str().is_none()
                    || !url.username().is_empty()
                    || url.password().is_some()
                {
                    return Err("Укажите HTTPS-ссылку Nextcloud".into());
                }
                if password.len() > 512 {
                    return Err("Пароль ссылки слишком длинный".into());
                }
                let path = url.path().trim_end_matches('/');
                let (base, token) = path
                    .rsplit_once("/s/")
                    .ok_or("Ссылка Nextcloud должна заканчиваться на /s/код")?;
                if !(8..=128).contains(&token.len())
                    || !token
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'-'))
                {
                    return Err("Неверный код ссылки Nextcloud".into());
                }
                let base = base.trim_end_matches("/index.php");
                let dav = format!("{base}/public.php/dav/files/{token}/");
                url.set_path(&dav);
                url.set_query(None);
                url.set_fragment(None);
                Ok(Self {
                    provider: "nextcloud".into(),
                    endpoint: url.into(),
                    user: "anonymous".into(),
                    password,
                })
            }
            Login::Koofr { email, password } => {
                let email = email.trim();
                if email.len() > 254
                    || !email.contains('@')
                    || email.contains(char::is_whitespace)
                    || password.is_empty()
                    || password.len() > 512
                {
                    return Err("Введите почту Koofr и пароль приложения".into());
                }
                Ok(Self {
                    provider: "koofr".into(),
                    endpoint: "https://app.koofr.net/dav/Koofr/YaJarvis/".into(),
                    user: email.into(),
                    password,
                })
            }
        }
    }

    fn request(&self, method: Method, name: &str) -> Result<reqwest::RequestBuilder, String> {
        let url = Url::parse(&self.endpoint)
            .and_then(|base| base.join(name))
            .map_err(|_| "Неверный адрес папки")?;
        let mut request = client()
            .request(method, url)
            .header("X-Requested-With", "XMLHttpRequest");
        if !self.password.is_empty() {
            request = request.basic_auth(&self.user, Some(&self.password));
        }
        Ok(request)
    }

    async fn verify(&self) -> Result<(), String> {
        if self.provider == "koofr" {
            let method = Method::from_bytes(b"MKCOL").unwrap();
            let status = self
                .request(method, "")?
                .send()
                .await
                .map_err(|e| net::short(&e))?
                .status();
            if !status.is_success() && status != StatusCode::METHOD_NOT_ALLOWED {
                return Err(cloud_error(status));
            }
        }
        let name = format!("yajarvis-connection-{}.txt", rand_hex(4));
        self.put(
            &name,
            "Проверка подключения YaJarvis\n".as_bytes().to_vec(),
            "text/plain; charset=utf-8",
            false,
        )
        .await?;
        if self.provider == "nextcloud" {
            let status = self
                .request(Method::GET, &name)?
                .send()
                .await
                .map_err(|e| net::short(&e))?
                .status();
            if status.is_success() || status.is_redirection() {
                return Err("Ссылка позволяет открывать файлы. В настройках папки выберите «Только загрузка»".into());
            }
            if !matches!(
                status,
                StatusCode::FORBIDDEN
                    | StatusCode::UNAUTHORIZED
                    | StatusCode::NOT_FOUND
                    | StatusCode::METHOD_NOT_ALLOWED
            ) {
                return Err(cloud_error(status));
            }
        }
        Ok(())
    }

    async fn put(&self, name: &str, bytes: Vec<u8>, content_type: &str, idempotent: bool) -> Result<(), String> {
        let response = self
            .request(Method::PUT, name)?
            .header("Content-Type", content_type)
            .header("If-None-Match", "*")
            .body(bytes)
            .send()
            .await
            .map_err(|e| net::short(&e))?;
        if !(response.status().is_success() || idempotent && response.status() == StatusCode::PRECONDITION_FAILED) {
            return Err(cloud_error(response.status()));
        }
        Ok(())
    }
}

pub struct ShortUpload {
    destination: Arc<Destination>,
    cancel: tokio::sync::watch::Receiver<u64>,
}

impl ShortUpload {
    pub async fn put(mut self, name: &str, zip: Vec<u8>) -> Result<(), String> {
        tokio::select! {
            biased;
            _ = self.cancel.changed() => Err("Подключение к облаку изменилось".into()),
            result = self.destination.put(name, zip, "application/zip", true) => result,
        }
    }
}

fn cloud_error(status: StatusCode) -> String {
    match status.as_u16() {
        401 | 403 => "Облако не разрешило загрузку. Проверьте ссылку или пароль".into(),
        404 | 409 => "Папка облака недоступна".into(),
        413 => "Облако отклонило файл по размеру".into(),
        429 => "Облако ограничило частоту загрузки. Попробуйте позже".into(),
        507 => "В облаке закончилось место".into(),
        code => format!("Облако ответило {code}"),
    }
}

#[derive(Default)]
struct State {
    destination: Option<Arc<Destination>>,
    until: Option<Instant>,
    samples: Vec<i16>,
    uploading: bool,
    sent: u32,
    discarded: u32,
    error: String,
    generation: u64,
    closed: bool,
}

impl State {
    fn stop(&mut self) {
        self.generation = self.generation.wrapping_add(1);
        self.until = None;
        self.samples = Vec::new();
        self.uploading = false;
    }
}

pub struct Recorder {
    config: PathBuf,
    state: Mutex<State>,
    cancel: tokio::sync::watch::Sender<u64>,
    cancel_short: tokio::sync::watch::Sender<u64>,
    configuring: tokio::sync::Mutex<()>,
}

impl Recorder {
    pub fn new(config: PathBuf) -> Self {
        let destination = fs::read(&config)
            .ok()
            .and_then(|bytes| serde_json::from_slice(&bytes).ok())
            .map(Arc::new);
        Self {
            config,
            state: Mutex::new(State {
                destination,
                ..State::default()
            }),
            cancel: tokio::sync::watch::channel(0).0,
            cancel_short: tokio::sync::watch::channel(0).0,
            configuring: tokio::sync::Mutex::new(()),
        }
    }

    pub async fn connect(&self, login: Login) -> Result<(), String> {
        let _gate = self.configuring.lock().await;
        let destination = Destination::from_login(login)?;
        {
            let s = self.state.lock().unwrap();
            if s.closed || s.until.is_some() {
                return Err("Сначала остановите запись фона".into());
            }
        }
        destination.verify().await?;
        let mut s = self.state.lock().unwrap();
        if s.closed || s.until.is_some() {
            return Err("Плагин выключен или уже записывает фон".into());
        }
        let bytes = serde_json::to_vec(&destination).map_err(|e| e.to_string())?;
        fs::create_dir_all(self.config.parent().unwrap()).map_err(|e| e.to_string())?;
        write_atomic(&self.config, &bytes, 0o600).map_err(|e| e.to_string())?;
        s.destination = Some(Arc::new(destination));
        self.cancel_short.send_modify(|n| *n = n.wrapping_add(1));
        s.error.clear();
        Ok(())
    }

    pub async fn disconnect(&self) -> Result<(), String> {
        let _gate = self.configuring.lock().await;
        let mut s = self.state.lock().unwrap();
        match fs::remove_file(&self.config) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e.to_string()),
        }
        s.stop();
        s.destination = None;
        self.cancel_short.send_modify(|n| *n = n.wrapping_add(1));
        s.error.clear();
        self.cancel.send_modify(|n| *n = n.wrapping_add(1));
        Ok(())
    }

    pub fn start(self: &Arc<Self>, minutes: u32) -> Result<(), String> {
        if !(1..=720).contains(&minutes) {
            return Err("Укажите от 1 до 720 минут".into());
        }
        let duration = Duration::from_secs(u64::from(minutes) * 60);
        let generation = {
            let mut s = self.state.lock().unwrap();
            if s.closed || s.until.is_some() {
                return Err("Фон уже записывается или плагин выключен".into());
            }
            if s.destination.is_none() {
                return Err("Сначала подключите папку в облаке".into());
            }
            s.generation = s.generation.wrapping_add(1);
            s.until = Some(Instant::now() + duration);
            s.sent = 0;
            s.discarded = 0;
            s.error.clear();
            s.generation
        };
        let weak = Arc::downgrade(self);
        tokio::spawn(async move {
            tokio::time::sleep(duration + Duration::from_secs(30)).await;
            if let Some(recorder) = weak.upgrade() {
                let mut state = recorder.state.lock().unwrap();
                if state.generation == generation {
                    state.stop();
                    recorder.cancel.send_modify(|n| *n = n.wrapping_add(1));
                }
            }
        });
        Ok(())
    }

    pub fn stop(&self) {
        let mut s = self.state.lock().unwrap();
        s.stop();
        self.cancel.send_modify(|n| *n = n.wrapping_add(1));
    }

    pub fn close(&self) {
        let mut s = self.state.lock().unwrap();
        s.closed = true;
        s.stop();
        self.cancel.send_modify(|n| *n = n.wrapping_add(1));
        self.cancel_short.send_modify(|n| *n = n.wrapping_add(1));
    }

    pub fn short_upload(&self) -> Option<ShortUpload> {
        let state = self.state.lock().unwrap();
        let destination = state.destination.clone()?;
        (!state.closed).then(|| ShortUpload { destination, cancel: self.cancel_short.subscribe() })
    }

    pub fn status(&self) -> Value {
        let s = self.state.lock().unwrap();
        json!({"provider":s.destination.as_ref().map(|d| &d.provider),"running":s.until.is_some(),"uploading":s.uploading,
            "sent":s.sent,"discarded":s.discarded,"left_sec":s.until.map_or(0, |until| until.saturating_duration_since(Instant::now()).as_secs()),"error":s.error})
    }

    pub fn frame(self: &Arc<Self>, rt: &tokio::runtime::Handle, pcm: &[i16], discard: bool) {
        let mut s = self.state.lock().unwrap();
        let Some(until) = s.until else { return };
        let expired = Instant::now() >= until;
        if s.uploading {
            return;
        }
        if discard {
            if !s.samples.is_empty() {
                s.samples.clear();
                s.discarded += 1;
            }
            if expired {
                s.stop();
            }
            return;
        }
        if !expired {
            if s.samples.capacity() == 0 {
                s.samples.reserve_exact(MINUTE);
            }
            let n = pcm.len().min(MINUTE - s.samples.len());
            s.samples.extend_from_slice(&pcm[..n]);
        }
        if s.samples.len() < MINUTE && (!expired || s.samples.len() < RATE) {
            if expired {
                s.stop();
            }
            return;
        }
        let mut cancel = self.cancel.subscribe();
        let samples = std::mem::take(&mut s.samples);
        let destination = s.destination.as_ref().unwrap().clone();
        let generation = s.generation;
        s.uploading = true;
        drop(s);
        let recorder = self.clone();
        rt.spawn(async move {
            let name = format!(
                "jarvis-bg-{}-{}",
                jiff::Timestamp::now().strftime("%Y%m%d-%H%M%S"),
                rand_hex(4)
            );
            let archive = format!("{name}.zip");
            let packed = tokio::task::spawn_blocking(move || pack(&name, &samples)).await;
            let result = match packed {
                Ok(Ok(zip)) => tokio::select! {
                    biased;
                    _ = cancel.changed() => return,
                    result = destination.put(&archive, zip, "application/zip", false) => result,
                },
                Ok(Err(e)) => Err(e.to_string()),
                Err(e) => Err(e.to_string()),
            };
            let mut s = recorder.state.lock().unwrap();
            if s.generation != generation {
                return;
            }
            s.uploading = false;
            match result {
                Ok(()) => {
                    s.sent += 1;
                    if expired || Instant::now() >= until {
                        s.stop();
                    }
                }
                Err(error) => {
                    s.error = error;
                    s.discarded += 1;
                    s.stop();
                }
            }
        });
    }
}

fn pack(name: &str, samples: &[i16]) -> zip::result::ZipResult<Vec<u8>> {
    let mut zip = zip::ZipWriter::new(Cursor::new(Vec::new()));
    let options = zip::write::SimpleFileOptions::default()
        .compression_method(zip::CompressionMethod::Deflated)
        .compression_level(Some(3));
    zip.start_file(format!("bg/{name}.wav"), options)?;
    zip.write_all(&pcm_wav(samples))?;
    Ok(zip.finish()?.into_inner())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nextcloud_link_is_mapped_to_its_upload_folder() {
        let cloud = Destination::from_login(Login::Nextcloud {
            link: "https://cloud.example.com/cloud/index.php/s/secrettoken?ignored=1".into(),
            password: String::new(),
        })
        .unwrap();
        assert_eq!(
            cloud.endpoint,
            "https://cloud.example.com/cloud/public.php/dav/files/secrettoken/"
        );
        assert!(
            Destination::from_login(Login::Nextcloud {
                link: "http://cloud.example.com/s/secrettoken".into(),
                password: String::new()
            })
            .is_err()
        );
        assert!(
            Destination::from_login(Login::Nextcloud {
                link: "https://name@cloud.example.com/s/secrettoken".into(),
                password: String::new()
            })
            .is_err()
        );
    }

    #[tokio::test]
    async fn preflight_and_minute_upload_use_webdav_without_writing_audio_to_disk() {
        use axum::{
            Router,
            body::Bytes,
            routing::{get, put},
        };
        use std::io::Read;
        let (tx, mut rx) = tokio::sync::mpsc::channel::<Bytes>(1);
        let app = Router::new().route(
            "/{name}",
            get(|| async { StatusCode::FORBIDDEN }).merge(put(move |data: Bytes| {
                let tx = tx.clone();
                async move {
                    if data.starts_with(b"PK") {
                        tx.send(data).await.unwrap();
                    }
                    StatusCode::CREATED
                }
            })),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        let destination = Arc::new(Destination {
            provider: "nextcloud".into(),
            endpoint: url,
            user: "anonymous".into(),
            password: String::new(),
        });
        destination.verify().await.unwrap();
        let path = std::env::temp_dir().join(format!("background-{}.json", rand_hex(6)));
        let recorder = Arc::new(Recorder::new(path.clone()));
        recorder.state.lock().unwrap().destination = Some(destination);
        recorder.start(2).unwrap();
        let runtime = tokio::runtime::Handle::current();
        recorder.frame(&runtime, &vec![5; RATE], false);
        recorder.frame(&runtime, &[0], true);
        assert_eq!(recorder.status()["discarded"], 1);
        recorder.frame(&runtime, &vec![7; MINUTE], false);
        let data = tokio::time::timeout(Duration::from_secs(10), rx.recv())
            .await
            .unwrap()
            .unwrap();
        let mut zip = zip::ZipArchive::new(Cursor::new(data)).unwrap();
        let mut file = zip.by_index(0).unwrap();
        let mut bytes = Vec::new();
        file.read_to_end(&mut bytes).unwrap();
        assert_eq!(&bytes[..4], b"RIFF");
        assert_eq!(&bytes[44..46], &7i16.to_le_bytes());
        assert_eq!(bytes.len(), 44 + 2 * MINUTE);
        assert!(!path.exists());
        recorder.close();
        assert_eq!(recorder.status()["running"], false);
        server.abort();
    }
}
