use super::{Manifest, valid_tool_name};
use jarvis_plugin_sdk::process::{API, read_frame, write_frame};
use serde_json::{Map, Value, json};
use std::{path::Path, process::Stdio, sync::Arc, time::Duration};
use tokio::{
    process::{ChildStdin, ChildStdout, Command},
    sync::{mpsc, oneshot, watch},
};

struct Call {
    method: String,
    params: Value,
    timeout: Duration,
    reply: Option<oneshot::Sender<Result<Value, String>>>,
}

async fn exchange(
    input: &mut ChildStdin,
    output: &mut ChildStdout,
    method: &str,
    params: Value,
) -> Result<Result<Value, String>, String> {
    write_frame(input, &json!({"method": method, "params": params}))
        .await
        .map_err(|e| e.to_string())?;
    let reply = read_frame(output).await.map_err(|e| e.to_string())?;
    if let Some(error) = reply["error"].as_str() {
        return Ok(Err(super::clip(error, 300)));
    }
    reply
        .get("result")
        .cloned()
        .map(Ok)
        .ok_or("Неверный ответ Rust-плагина".into())
}

pub fn check_binary(bytes: &[u8], arch: &str) -> Result<(), String> {
    let elf = match arch {
        "arm64" => (2, 183),
        "armv7" => (1, 40),
        "x86_64" => (2, 62),
        _ => return Err("Архитектура Rust-плагина не поддерживается".into()),
    };
    if bytes.len() < 20
        || !bytes.starts_with(b"\x7fELF")
        || bytes[5] != 1
        || (bytes[4], u16::from_le_bytes([bytes[18], bytes[19]])) != elf
    {
        return Err("Исполняемый файл Rust-плагина не подходит для этой архитектуры".into());
    }
    Ok(())
}

pub struct Process {
    tx: mpsc::Sender<Call>,
    stop: watch::Sender<bool>,
    running: watch::Receiver<bool>,
    tools: Vec<Value>,
    chat: bool,
    stt: bool,
    tts: bool,
}

impl Process {
    pub async fn start(
        manifest: &Manifest,
        dir: &Path,
        info: Value,
        settings: Map<String, Value>,
    ) -> Result<Arc<Self>, String> {
        let exe = dir.join(&manifest.main);
        if !exe.symlink_metadata().is_ok_and(|meta| meta.is_file()) {
            return Err("Исполняемый файл Rust-плагина отсутствует".into());
        }
        let exe = exe.canonicalize().map_err(|e| e.to_string())?;
        let data = dir.join("data");
        tokio::fs::create_dir_all(&data)
            .await
            .map_err(|e| e.to_string())?;
        let mut child = Command::new(exe)
            .current_dir(data)
            .env_clear()
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .kill_on_drop(true)
            .spawn()
            .map_err(|e| format!("Не удалось запустить Rust-плагин: {e}"))?;
        let mut input = child.stdin.take().ok_or("Нет stdin плагина")?;
        let mut output = child.stdout.take().ok_or("Нет stdout плагина")?;
        let init = tokio::time::timeout(
            Duration::from_secs(5),
            exchange(
                &mut input,
                &mut output,
                "init",
                json!({"api":API,"info":info,"settings":settings}),
            ),
        )
        .await
        .map_err(|_| "Rust-плагин не подтвердил запуск")???;
        if init["api"] != API {
            return Err("Rust-плагин подтвердил другую версию протокола".into());
        }
        let tools = init["tools"]
            .as_array()
            .into_iter()
            .flatten()
            .filter(|tool| {
                tool["type"] == "function"
                    && tool["name"].as_str().is_some_and(valid_tool_name)
                    && tool["parameters"].is_object()
            })
            .take(64)
            .cloned()
            .collect();
        let (tx, mut calls) = mpsc::channel::<Call>(8);
        let (stop, mut stopped) = watch::channel(false);
        let (alive, running) = watch::channel(true);
        tokio::spawn(async move {
            loop {
                let call = tokio::select! {
                    _ = stopped.changed() => {
                        let shutdown = async {
                            let _ = write_frame(&mut input, &json!({"method":"shutdown"})).await;
                            let _ = child.wait().await;
                        };
                        let _ = tokio::time::timeout(Duration::from_secs(1), shutdown).await;
                        break;
                    }
                    _ = child.wait() => break,
                    call = calls.recv() => match call { Some(call) => call, None => break },
                };
                if call.reply.as_ref().is_some_and(|reply| reply.is_closed()) {
                    continue;
                }
                let result = tokio::select! {
                    _ = stopped.changed() => Err("Rust-плагин остановлен".into()),
                    result = tokio::time::timeout(call.timeout, exchange(&mut input, &mut output, &call.method, call.params)) =>
                        result.unwrap_or_else(|_| Err("Rust-плагин не ответил вовремя".into())),
                };
                let broken = result.is_err();
                if let Some(reply) = call.reply {
                    let _ = reply.send(result.and_then(|value| value));
                }
                if broken {
                    break;
                }
            }
            let _ = child.kill().await;
            let _ = alive.send(false);
        });
        Ok(Arc::new(Self {
            tx,
            stop,
            running,
            tools,
            chat: manifest.provides("llm"),
            stt: manifest.provides("stt"),
            tts: manifest.provides("tts"),
        }))
    }

    pub fn running(&self) -> bool {
        *self.running.borrow()
    }
    pub fn supports_chat(&self) -> bool {
        self.chat && self.running()
    }
    pub fn supports_stt(&self) -> bool {
        self.stt && self.running()
    }
    pub fn supports_tts(&self) -> bool {
        self.tts && self.running()
    }
    pub fn tools(&self) -> Vec<Value> {
        if self.running() {
            self.tools.clone()
        } else {
            vec![]
        }
    }
    pub async fn request(
        &self,
        method: &str,
        params: Value,
        timeout: Duration,
    ) -> Result<Value, String> {
        let (reply, result) = oneshot::channel();
        self.tx
            .try_send(Call {
                method: method.into(),
                params,
                timeout,
                reply: Some(reply),
            })
            .map_err(|_| "Rust-плагин занят или остановлен")?;
        tokio::time::timeout(timeout, result)
            .await
            .map_err(|_| "Rust-плагин не ответил вовремя")?
            .map_err(|_| "Rust-плагин остановлен")?
    }
    pub async fn call_tool(&self, name: &str, args: &str) -> Option<String> {
        if !self.running() || !self.tools.iter().any(|tool| tool["name"] == name) {
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
                    json!({"name":name,"arguments":arguments}),
                    Duration::from_secs(15),
                )
                .await
            {
                Ok(Value::String(answer)) => answer,
                Ok(value) => value.to_string(),
                Err(err) => err,
            },
        )
    }
    pub fn configure(&self, values: Map<String, Value>) {
        let _ = self.tx.try_send(Call {
            method: "configure".into(),
            params: values.into(),
            timeout: Duration::from_secs(5),
            reply: None,
        });
    }
    pub async fn stop(&self) {
        self.stop.send_replace(true);
        let mut running = self.running.clone();
        let _ = running.wait_for(|alive| !*alive).await;
    }
}
