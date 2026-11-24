use super::{App, bad, ok};
use crate::{settings::Settings, vpn::Route};
use axum::{Json, extract::State, http::StatusCode, response::Response};
use serde_json::{Map, Value, json};
use std::{
    collections::HashMap,
    time::{Duration, Instant},
};

const PROBE_TIMEOUT: Duration = Duration::from_secs(12);

struct Probe {
    id: &'static str,
    route_svc: &'static str,
    url: String,
    header: Option<(&'static str, String)>,
}

pub async fn put(State(app): State<App>, Json(input): Json<Value>) -> Response {
    let Some(link) = input.get("link").and_then(Value::as_str) else {
        return bad("Нужен параметр link");
    };
    match app.proxy.set_link(&app.plugins, link).await {
        Ok(()) => ok(),
        Err(e) => bad(e),
    }
}

fn select_probes(s: &Settings, youtube_available: bool) -> Vec<Probe> {
    let mut probes = Vec::new();
    if crate::chatgpt::signed_in(&s.chatgpt) {
        probes.push(Probe {
            id: "chatgpt",
            route_svc: "chatgpt",
            url: "https://auth.openai.com/.well-known/openid-configuration".into(),
            header: None,
        });
    }
    if !s.openai_key.is_empty() {
        probes.push(Probe {
            id: "openai",
            route_svc: "openai",
            url: "https://api.openai.com/v1/models".into(),
            header: Some(("Authorization", format!("Bearer {}", s.openai_key))),
        });
    }
    if !s.gemini_key.is_empty() {
        probes.push(Probe {
            id: "gemini",
            route_svc: "gemini",
            url: "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1".into(),
            header: Some(("x-goog-api-key", s.gemini_key.clone())),
        });
    }
    if !s.grok_key.is_empty() {
        probes.push(Probe {
            id: "grok",
            route_svc: "grok",
            url: "https://api.x.ai/v1/models".into(),
            header: Some(("Authorization", format!("Bearer {}", s.grok_key))),
        });
    }
    if !s.elevenlabs_key.is_empty() {
        probes.push(Probe {
            id: "elevenlabs",
            route_svc: "elevenlabs",
            url: "https://api.elevenlabs.io/v2/voices?page_size=1".into(),
            header: Some(("xi-api-key", s.elevenlabs_key.clone())),
        });
    }
    if youtube_available {
        probes.push(Probe {
            id: "youtube",
            route_svc: "youtube",
            url: "https://www.youtube.com/generate_204".into(),
            header: None,
        });
    }
    if probes.is_empty() && !s.vless.trim().is_empty() {
        probes.push(Probe {
            id: "vpn",
            route_svc: "",
            url: "https://www.gstatic.com/generate_204".into(),
            header: None,
        });
    }
    probes
}

pub(super) fn route_client(route: &Route, timeout: Duration) -> Result<reqwest::Client, String> {
    let mut builder = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .user_agent(crate::net::user_agent())
        .connect_timeout(Duration::from_secs(10))
        .timeout(timeout);
    match route {
        Route::Direct => {}
        Route::Tunnel(t) => {
            let proxy = reqwest::Proxy::all(format!("socks5h://{}", t.socks))
                .map_err(|e| e.to_string())?
                .basic_auth(&t.user, &t.pass);
            builder = builder.proxy(proxy);
        }
        Route::Blocked(reason) => return Err(reason.clone()),
    }
    builder.build().map_err(|e| e.to_string())
}

fn gemini_key_invalid(body: &[u8]) -> bool {
    if let Ok(value) = serde_json::from_slice::<Value>(body) {
        let details_invalid = value["error"]["details"]
            .as_array()
            .is_some_and(|details| details.iter().any(|detail| detail["reason"] == "API_KEY_INVALID"));
        let message_invalid = value["error"]["message"]
            .as_str()
            .is_some_and(|message| message.to_ascii_lowercase().contains("api key not valid"));
        return details_invalid || message_invalid;
    }
    let text = String::from_utf8_lossy(body);
    text.contains("API_KEY_INVALID") || text.to_ascii_lowercase().contains("api key not valid")
}

async fn run_probe(route: Route, probe: Probe) -> (&'static str, Value) {
    let client = match route_client(&route, PROBE_TIMEOUT) {
        Ok(client) => client,
        Err(error) => return (probe.id, json!({"ok": false, "error": error})),
    };
    let started = Instant::now();
    let mut request = client.get(&probe.url);
    if let Some((name, value)) = probe.header {
        request = request.header(name, value);
    }
    let mut resp = match request.send().await {
        Ok(resp) => resp,
        Err(e) => return (probe.id, json!({"ok": false, "error": crate::net::short(&e)})),
    };
    let code = resp.status().as_u16();
    let ms = started.elapsed().as_millis() as u64;
    let ok = (200..300).contains(&code);
    let key_invalid = if probe.id == "gemini" && code == 400 {
        let mut body = Vec::new();
        while body.len() < 16 * 1024 {
            match resp.chunk().await {
                Ok(Some(chunk)) => {
                    let remaining = (16 * 1024) - body.len();
                    body.extend_from_slice(&chunk[..chunk.len().min(remaining)]);
                }
                Ok(None) | Err(_) => break,
            }
        }
        gemini_key_invalid(&body)
    } else {
        false
    };
    let mut res = json!({"ok": ok, "code": code, "ms": ms});
    if code == 401 || (probe.id == "gemini" && code == 400 && key_invalid) {
        res["error"] = json!("ключ не подошёл");
    } else if code == 403 {
        res["error"] = json!("доступ запрещён — похоже, сервис видит российский/белорусский адрес, нужен VLESS");
    } else if !ok {
        res["error"] = json!(format!("ответ {code}"));
    }
    (probe.id, res)
}

/// Проверить доступность настроенных сервисов (и VPN при отсутствии сервисов) через текущие маршруты.
pub async fn test_net(State(app): State<App>) -> Response {
    let s = app.store.get();
    let youtube = app.plugins.provider("youtube").is_ok();
    let probes = select_probes(&s, youtube);
    let order: Vec<&'static str> = probes.iter().map(|p| p.id).collect();
    let mut set = tokio::task::JoinSet::new();
    for probe in probes {
        let route = app.proxy.route(probe.route_svc);
        set.spawn(run_probe(route, probe));
    }
    let mut results = HashMap::with_capacity(order.len());
    while let Some(joined) = set.join_next().await {
        if let Ok((id, value)) = joined {
            results.insert(id, value);
        }
    }
    let mut out = Map::with_capacity(order.len());
    for id in order {
        if let Some(value) = results.remove(id) {
            out.insert(id.to_string(), value);
        }
    }
    super::reply(StatusCode::OK, Value::Object(out))
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[test]
    fn select_probes_covers_configured_services_and_vpn_fallback() {
        let mut s = Settings::default();
        assert!(select_probes(&s, false).is_empty());

        s.vless = "vless://00112233-4455-6677-8899-aabbccddeeff@example.com:443".into();
        let vpn_only = select_probes(&s, false);
        assert_eq!(vpn_only.len(), 1);
        assert_eq!(vpn_only[0].id, "vpn");
        assert_eq!(vpn_only[0].route_svc, "");

        s.chatgpt = json!({"refresh": "rt", "account": "acc"});
        s.gemini_key = "gem-key".into();
        let partial = select_probes(&s, true);
        let partial_ids: Vec<_> = partial.iter().map(|p| p.id).collect();
        assert_eq!(partial_ids, vec!["chatgpt", "gemini", "youtube"]);
        assert!(!partial_ids.contains(&"openai"));

        s.openai_key = "oa-key".into();
        s.grok_key = "grok-key".into();
        s.elevenlabs_key = "el-key".into();
        let all = select_probes(&s, true);
        let all_ids: Vec<_> = all.iter().map(|p| p.id).collect();
        assert_eq!(
            all_ids,
            vec!["chatgpt", "openai", "gemini", "grok", "elevenlabs", "youtube"]
        );
    }

    #[tokio::test]
    async fn run_probe_reports_blocked_route_without_network() {
        let probe = Probe {
            id: "openai",
            route_svc: "openai",
            url: "http://127.0.0.1:1/unreachable".into(),
            header: None,
        };
        let (id, res) = run_probe(Route::Blocked("плагин VPN запускается".into()), probe).await;
        assert_eq!(id, "openai");
        assert_eq!(res["ok"], false);
        assert_eq!(res["error"], "плагин VPN запускается");
    }

    #[tokio::test]
    async fn run_probe_maps_http_statuses_and_headers() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = tokio::spawn(async move {
            let responses = [
                ("HTTP/1.1 200 OK", "{}"),
                ("HTTP/1.1 204 No Content", ""),
                ("HTTP/1.1 401 Unauthorized", "{}"),
                ("HTTP/1.1 403 Forbidden", "{}"),
                (
                    "HTTP/1.1 400 Bad Request",
                    r#"{"error":{"details":[{"reason":"API_KEY_INVALID"}]}}"#,
                ),
                ("HTTP/1.1 500 Internal Server Error", "err"),
            ];
            for (idx, (status_line, body)) in responses.into_iter().enumerate() {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut raw = Vec::new();
                while !raw.ends_with(b"\r\n\r\n") {
                    raw.push(socket.read_u8().await.unwrap());
                    assert!(raw.len() < 8192);
                }
                if idx == 0 {
                    let headers = String::from_utf8(raw).unwrap().to_ascii_lowercase();
                    assert!(headers.contains("authorization: bearer test-token\r\n"));
                }
                let payload = format!(
                    "{status_line}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                socket.write_all(payload.as_bytes()).await.unwrap();
                socket.shutdown().await.unwrap();
            }
        });

        let base = format!("http://{addr}");

        let (id, ok_res) = run_probe(
            Route::Direct,
            Probe {
                id: "openai",
                route_svc: "openai",
                url: format!("{base}/ok"),
                header: Some(("Authorization", "Bearer test-token".into())),
            },
        )
        .await;
        assert_eq!(id, "openai");
        assert_eq!(ok_res["ok"], true);
        assert_eq!(ok_res["code"], 200);
        assert!(ok_res["ms"].is_u64());
        assert!(ok_res.get("error").is_none());

        let (id, no_content) = run_probe(
            Route::Direct,
            Probe {
                id: "youtube",
                route_svc: "youtube",
                url: format!("{base}/204"),
                header: None,
            },
        )
        .await;
        assert_eq!(id, "youtube");
        assert_eq!(no_content["ok"], true);
        assert_eq!(no_content["code"], 204);
        assert!(no_content.get("error").is_none());

        let (_, unauth) = run_probe(
            Route::Direct,
            Probe {
                id: "openai",
                route_svc: "openai",
                url: format!("{base}/401"),
                header: None,
            },
        )
        .await;
        assert_eq!(unauth["ok"], false);
        assert_eq!(unauth["code"], 401);
        assert_eq!(unauth["error"], "ключ не подошёл");

        let (_, forbidden) = run_probe(
            Route::Direct,
            Probe {
                id: "grok",
                route_svc: "grok",
                url: format!("{base}/403"),
                header: None,
            },
        )
        .await;
        assert_eq!(forbidden["ok"], false);
        assert_eq!(forbidden["code"], 403);
        assert!(forbidden["error"].as_str().unwrap().contains("VLESS"));

        let (_, gem_invalid) = run_probe(
            Route::Direct,
            Probe {
                id: "gemini",
                route_svc: "gemini",
                url: format!("{base}/400"),
                header: Some(("x-goog-api-key", "bad".into())),
            },
        )
        .await;
        assert_eq!(gem_invalid["ok"], false);
        assert_eq!(gem_invalid["code"], 400);
        assert_eq!(gem_invalid["error"], "ключ не подошёл");

        let (_, server_err) = run_probe(
            Route::Direct,
            Probe {
                id: "elevenlabs",
                route_svc: "elevenlabs",
                url: format!("{base}/500"),
                header: None,
            },
        )
        .await;
        assert_eq!(server_err["ok"], false);
        assert_eq!(server_err["code"], 500);
        assert_eq!(server_err["error"], "ответ 500");

        server.await.unwrap();
    }
}
