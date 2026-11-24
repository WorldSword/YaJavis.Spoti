use crate::{Callback, Result};
use futures_util::{SinkExt, StreamExt};
use jarvis_cloud::{Message, Provider};
use serde_json::{Value, json};
use std::sync::{
    Arc, Mutex,
    atomic::{AtomicU64, Ordering},
};
use tokio::{sync::mpsc, task::AbortHandle};

pub struct Cloud {
    provider: Provider,
    services: Callback,
    current: Mutex<Option<Arc<Live>>>,
    next: AtomicU64,
}

struct Live {
    id: u64,
    send: mpsc::Sender<Value>,
    received: tokio::sync::Mutex<mpsc::Receiver<Result>>,
    task: AbortHandle,
}

impl Drop for Live {
    fn drop(&mut self) {
        self.task.abort();
    }
}

impl Cloud {
    pub fn new(id: &str, services: Callback) -> std::result::Result<Self, String> {
        Ok(Self {
            provider: Provider::new(id)?,
            services,
            current: Mutex::new(None),
            next: AtomicU64::new(1),
        })
    }

    pub async fn request(&self, method: &str, params: Value) -> Result {
        if method == self.provider.id() {
            let network = (self.services)("network".into(), Value::Null).await?;
            return self.provider.request(params, network).await;
        }
        if method == "live.open" {
            let key = params["key"].as_str().ok_or("Нет API-ключа")?;
            let network = (self.services)("network".into(), Value::Null).await?;
            let session = params.get("session").filter(|value| !value.is_null());
            let mut socket = self.provider.socket_for(key, &network, session).await?;
            let (send, mut outgoing) = mpsc::channel::<Value>(8);
            let (incoming, received) = mpsc::channel(12);
            let id = self.next.fetch_add(1, Ordering::Relaxed);
            let task = tokio::spawn(async move {
                loop {
                    tokio::select! {
                        value = outgoing.recv() => {
                            let Some(value) = value else { break };
                            if socket.send(value.to_string().into()).await.is_err() {
                                let _ = incoming.send(Err("Обрыв голосового соединения".into())).await;
                                break;
                            }
                        }
                        message = socket.next() => match message {
                            Some(Ok(Message::Close(reason))) => {
                                let reason = reason.map(|reason| reason.reason.to_string()).unwrap_or_default();
                                let _ = incoming.send(Ok(json!({"close":reason}))).await;
                                break;
                            }
                            Some(Ok(message)) if message.is_text() || message.is_binary() => {
                                let event = serde_json::from_slice::<Value>(&message.into_data())
                                    .map(|event| json!({"event":event}))
                                    .map_err(|_| "Неверное голосовое событие".into());
                                if incoming.send(event).await.is_err() { break }
                            }
                            Some(Ok(_)) => {}
                            Some(Err(_)) => {
                                let _ = incoming.send(Err("Обрыв голосового соединения".into())).await;
                                break;
                            }
                            None => break,
                        }
                    }
                }
            }).abort_handle();
            let live = Arc::new(Live {
                id,
                send,
                received: tokio::sync::Mutex::new(received),
                task,
            });
            if let Some(previous) = self.current.lock().unwrap().replace(live) {
                previous.task.abort();
            }
            return Ok(json!({"id":id}));
        }
        let id = params["id"].as_u64().ok_or("Нет голосовой сессии")?;
        if method == "live.close" {
            let mut current = self.current.lock().unwrap();
            if current.as_ref().is_some_and(|live| live.id == id) {
                if let Some(live) = current.take() {
                    live.task.abort();
                }
            }
            return Ok(Value::Null);
        }
        let live = self
            .current
            .lock()
            .unwrap()
            .clone()
            .filter(|live| live.id == id)
            .ok_or("Голосовая сессия завершилась")?;
        match method {
            "live.send" => {
                live.send
                    .send(params["event"].clone())
                    .await
                    .map_err(|_| "Голосовая сессия завершилась")?;
                Ok(Value::Null)
            }
            "live.recv" => {
                let mut received = live.received.lock().await;
                match tokio::time::timeout(std::time::Duration::from_secs(12), received.recv())
                    .await
                {
                    Ok(value) => value.unwrap_or(Ok(Value::Null)),
                    Err(_) => Ok(json!({"idle":true})),
                }
            }
            _ => Err(format!("Неподдерживаемый метод {method}")),
        }
    }

    pub fn stop(&self) {
        if let Some(live) = self.current.lock().unwrap().take() {
            live.task.abort();
        }
        self.provider.close();
    }
}
