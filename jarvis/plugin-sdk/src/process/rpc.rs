//! Bidirectional RPC for official packages. Both peers can initiate a call.

use super::{read_frame, write_frame};
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    future::Future,
    pin::Pin,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};
use tokio::{
    io::{AsyncRead, AsyncWrite},
    sync::{mpsc, oneshot, watch},
    task::JoinSet,
};

pub type Result = std::result::Result<Value, String>;
pub type Handler =
    Box<dyn Fn(Peer, String, Value) -> Pin<Box<dyn Future<Output = Result> + Send>> + Send + Sync>;
pub const PROTOCOL: u64 = 2;
const IN_FLIGHT: usize = 24;

struct Inner {
    tx: mpsc::Sender<Value>,
    pending: Mutex<HashMap<u64, oneshot::Sender<Result>>>,
    next: AtomicU64,
    closed: watch::Sender<bool>,
}

#[derive(Clone)]
pub struct Peer(Arc<Inner>);

struct Pending(Peer, u64);
impl Drop for Pending {
    fn drop(&mut self) {
        self.0.0.pending.lock().unwrap().remove(&self.1);
    }
}

impl Peer {
    pub fn open(
        mut reader: impl AsyncRead + Unpin + Send + 'static,
        mut writer: impl AsyncWrite + Unpin + Send + 'static,
        handler: Handler,
    ) -> Self {
        let (tx, mut rx) = mpsc::channel(IN_FLIGHT);
        let (closed, _) = watch::channel(false);
        let peer = Self(Arc::new(Inner {
            tx,
            pending: Mutex::new(HashMap::new()),
            next: AtomicU64::new(1),
            closed,
        }));
        let link = peer.clone();
        tokio::spawn(async move {
            let mut calls = JoinSet::new();
            let read = async {
                loop {
                    let Ok(frame) = read_frame(&mut reader).await else {
                        break;
                    };
                    let Some(id) = frame["id"].as_u64() else {
                        break;
                    };
                    if frame["method"].is_string() {
                        while calls.try_join_next().is_some() {}
                        let mut call = frame;
                        let method = call["method"]
                            .take()
                            .as_str()
                            .unwrap_or_default()
                            .to_owned();
                        let tx = link.0.tx.clone();
                        if calls.len() >= IN_FLIGHT {
                            if tx
                                .try_send(json!({"id":id,"error":"Плагин занят"}))
                                .is_err()
                            {
                                break;
                            }
                            continue;
                        }
                        let future = handler(link.clone(), method, call["params"].take());
                        calls.spawn(async move {
                            let reply = match future.await {
                                Ok(value) => json!({"id":id,"result":value}),
                                Err(error) => json!({"id":id,"error":error}),
                            };
                            let _ = tx.send(reply).await;
                        });
                    } else if let Some(reply) = link.0.pending.lock().unwrap().remove(&id) {
                        let result = match frame["error"].as_str() {
                            Some(error) => Err(error.chars().take(400).collect()),
                            None => Ok(frame["result"].clone()),
                        };
                        let _ = reply.send(result);
                    }
                }
            };
            let write = async {
                while let Some(frame) = rx.recv().await {
                    if write_frame(&mut writer, &frame).await.is_err() {
                        break;
                    }
                }
            };
            tokio::select! {
                _ = link.closed() => {}
                _ = read => {}
                _ = write => {}
            }
            link.close();
            link.0.pending.lock().unwrap().clear();
        });
        peer
    }

    pub async fn call(&self, method: &str, params: Value, timeout: Duration) -> Result {
        let id = self.0.next.fetch_add(1, Ordering::Relaxed);
        let (reply, receiver) = oneshot::channel();
        {
            let mut pending = self.0.pending.lock().unwrap();
            if pending.len() >= IN_FLIGHT {
                return Err("Плагин занят".into());
            }
            pending.insert(id, reply);
        }
        let _pending = Pending(self.clone(), id);
        let request = async {
            self.0
                .tx
                .send(json!({"id":id,"method":method,"params":params}))
                .await
                .map_err(|_| "Процесс плагина завершился".to_string())?;
            receiver
                .await
                .map_err(|_| "Процесс плагина завершился".to_string())?
        };
        tokio::select! {
            _ = self.closed() => Err("Процесс плагина завершился".into()),
            result = tokio::time::timeout(timeout, request) => result.map_err(|_| "Плагин не ответил вовремя".to_string())?,
        }
    }

    pub fn running(&self) -> bool {
        !*self.0.closed.borrow()
    }

    pub fn close(&self) {
        self.0.closed.send_replace(true);
    }

    pub async fn closed(&self) {
        let _ = self.0.closed.subscribe().wait_for(|closed| *closed).await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{duplex, split};

    #[tokio::test]
    async fn callbacks_can_run_while_original_request_waits() {
        let (a, b) = duplex(8192);
        let (ar, aw) = split(a);
        let (br, bw) = split(b);
        let host = Peer::open(
            ar,
            aw,
            Box::new(|_, _, value| Box::pin(async { Ok(value) })),
        );
        let worker = Peer::open(
            br,
            bw,
            Box::new(|peer, _, value| {
                Box::pin(async move { peer.call("callback", value, Duration::from_secs(1)).await })
            }),
        );
        let input = json!({"value":17});
        assert_eq!(
            host.call("run", input.clone(), Duration::from_secs(1))
                .await
                .unwrap(),
            input
        );
        worker.close();
        host.closed().await;
    }
}
