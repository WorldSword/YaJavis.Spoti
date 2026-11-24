use super::*;
use base64::{Engine as _, engine::general_purpose::STANDARD as B64};
use std::{collections::BTreeMap, io::Read};
pub struct Timer {
    at: Instant,
    period: Option<Duration>,
    function: Function,
}
pub type Timers = Arc<Mutex<BTreeMap<u64, Timer>>>;
pub fn next_at(timers: &Timers) -> Option<Instant> {
    timers.lock().unwrap().values().map(|t| t.at).min()
}
pub fn due(timers: &Timers) -> Vec<(u64, Function)> {
    let mut timers = timers.lock().unwrap();
    let now = Instant::now();
    let keys: Vec<_> = timers
        .iter()
        .filter(|(_, t)| t.at <= now)
        .map(|(id, _)| *id)
        .collect();
    keys.into_iter()
        .filter_map(|id| {
            let t = timers.get_mut(&id)?;
            let f = t.function.clone();
            if let Some(period) = t.period {
                t.at = now + period;
            } else {
                timers.remove(&id);
            }
            Some((id, f))
        })
        .collect()
}
fn fail(error: impl std::fmt::Display) -> mlua::Error {
    mlua::Error::external(error.to_string())
}
fn bad_header_val(s: &str) -> bool {
    s.bytes().any(|b| b == b'\r' || b == b'\n' || b == 0)
}
fn encode_multipart(boundary: &str, parts: &[Value]) -> mlua::Result<Vec<u8>> {
    const MAX_MULTIPART: usize = 4 * 1024 * 1024;
    let mut out = Vec::new();
    for part in parts {
        let obj = part
            .as_object()
            .ok_or_else(|| fail("элемент multipart должен быть объектом"))?;
        let name = obj
            .get("name")
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty() && !bad_header_val(s) && !s.contains('"'))
            .ok_or_else(|| fail("неверное имя поля multipart"))?;
        out.extend_from_slice(b"--");
        out.extend_from_slice(boundary.as_bytes());
        out.extend_from_slice(b"\r\nContent-Disposition: form-data; name=\"");
        out.extend_from_slice(name.as_bytes());
        out.push(b'"');
        if let Some(filename) = obj.get("filename").and_then(Value::as_str) {
            if bad_header_val(filename) || filename.contains('"') {
                return Err(fail("неверное filename в multipart"));
            }
            out.extend_from_slice(b"; filename=\"");
            out.extend_from_slice(filename.as_bytes());
            out.push(b'"');
        }
        out.extend_from_slice(b"\r\n");
        if let Some(val) = obj.get("value").filter(|v| !v.is_null()) {
            let text = val
                .as_str()
                .ok_or_else(|| fail("значение поля multipart должно быть строкой"))?;
            out.extend_from_slice(b"\r\n");
            out.extend_from_slice(text.as_bytes());
        } else if let Some(b64) = obj.get("data_base64").filter(|v| !v.is_null()) {
            let b64 = b64
                .as_str()
                .ok_or_else(|| fail("data_base64 в multipart должен быть строкой"))?;
            let ct = obj
                .get("content_type")
                .and_then(Value::as_str)
                .unwrap_or("application/octet-stream");
            if ct.is_empty() || bad_header_val(ct) {
                return Err(fail("неверный content_type в multipart"));
            }
            let bytes = B64
                .decode(b64)
                .map_err(|_| fail("неверный data_base64 в multipart"))?;
            out.extend_from_slice(b"Content-Type: ");
            out.extend_from_slice(ct.as_bytes());
            out.extend_from_slice(b"\r\n\r\n");
            out.extend_from_slice(&bytes);
        } else {
            return Err(fail("в части multipart нужен value или data_base64"));
        }
        out.extend_from_slice(b"\r\n");
        if out.len() > MAX_MULTIPART {
            return Err(fail("multipart тело больше 4 МБ"));
        }
    }
    out.extend_from_slice(b"--");
    out.extend_from_slice(boundary.as_bytes());
    out.extend_from_slice(b"--\r\n");
    if out.len() > MAX_MULTIPART {
        return Err(fail("multipart тело больше 4 МБ"));
    }
    Ok(out)
}
fn permitted(url: &reqwest::Url, allowed: &[String]) -> bool {
    if !matches!(url.scheme(), "http" | "https")
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return false;
    }
    let Some(host) = url.host_str() else {
        return false;
    };
    allowed.iter().any(|a| {
        a == host
            || a == &url.origin().ascii_serialization()
            || a.strip_prefix("*.")
                .is_some_and(|suffix| host.ends_with(&format!(".{suffix}")))
    })
}
fn resolve_http_client(
    host: &Weak<Host>,
    runtime: &tokio::runtime::Handle,
    plugin_id: &str,
    is_service: bool,
    cached_client: &Mutex<Option<(Value, reqwest::Client)>>,
) -> mlua::Result<reqwest::Client> {
    let host_arc = host.upgrade().ok_or_else(|| fail("хост завершился"))?;
    let route = if is_service {
        runtime
            .block_on((host_arc.hooks.request)(plugin_id.to_owned(), "network".into(), Value::Null))
            .map_err(fail)?
    } else {
        (host_arc.hooks.network)()
    };
    if is_service && !route.is_object() {
        return Err(fail("Хост не предоставил сетевой маршрут"));
    }
    if let Some(error) = route["error"].as_str().filter(|s| !s.is_empty()) {
        return Err(fail(error));
    }
    let mut g = cached_client.lock().unwrap();
    if let Some((prev, client)) = &*g && *prev == route {
        return Ok(client.clone());
    }
    let mut builder = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(if is_service { 5 } else { 8 }))
        .timeout(Duration::from_secs(if is_service { 10 } else { 25 }));
    if let Some(socks) = route["socks"].as_str() {
        let address: std::net::SocketAddr = socks.parse().map_err(|_| fail("неверный SOCKS VPN"))?;
        let user = route["user"].as_str().unwrap_or("");
        let pass = route["pass"].as_str().unwrap_or("");
        if !address.ip().is_loopback()
            || address.port() == 0
            || (is_service && (user.is_empty() || user.len() > 255 || pass.is_empty() || pass.len() > 255))
        {
            return Err(fail("неверный SOCKS VPN"));
        }
        builder = builder.proxy(
            reqwest::Proxy::all(format!("socks5h://{socks}"))
                .map_err(fail)?
                .basic_auth(user, pass),
        );
    } else if is_service && (route.get("socks").is_some() || route.get("user").is_some() || route.get("pass").is_some()) {
        return Err(fail("неверный SOCKS VPN"));
    }
    let client = builder.build().map_err(fail)?;
    *g = Some((route, client.clone()));
    Ok(client)
}
pub fn install(
    lua: &Lua,
    api: &Table,
    m: &Manifest,
    host: Weak<Host>,
    stopped: Arc<AtomicBool>,
    runtime: tokio::runtime::Handle,
    cpu: Arc<Mutex<Instant>>,
    wall: Arc<Mutex<Instant>>,
) -> mlua::Result<Timers> {
    api.set(
        "now",
        lua.create_function(|_, ()| Ok(jiff::Timestamp::now().as_millisecond()))?,
    )?;
    api.set(
        "normalize",
        lua.create_function(|_, s: String| Ok(super::super::command_text(&s)))?,
    )?;
    api.set(
        "norm",
        lua.create_function(|_, s: String| Ok(crate::fastcmd::norm_text(&s)))?,
    )?;
    api.set(
        "random",
        lua.create_function(|_, (min, max): (i64, i64)| {
            let span = u64::try_from(max as i128 - min as i128 + 1)
                .map_err(|_| fail("неверный диапазон"))?;
            if span == 0 {
                return Err(fail("неверный диапазон"));
            }
            let limit = u64::MAX - u64::MAX % span;
            loop {
                let mut bytes = [0; 8];
                getrandom::fill(&mut bytes).map_err(fail)?;
                let n = u64::from_le_bytes(bytes);
                if n < limit {
                    return Ok((min as i128 + (n % span) as i128) as i64);
                }
            }
        })?,
    )?;
    api.set(
        "urlencode",
        lua.create_function(|_, text: String| {
            let mut out = String::new();
            for byte in text.bytes() {
                if byte.is_ascii_alphanumeric() || b"-_.~".contains(&byte) {
                    out.push(byte as char);
                } else {
                    out.push_str(&format!("%{byte:02X}"));
                }
            }
            Ok(out)
        })?,
    )?;
    if m.permissions.iter().any(|p| p == "storage") {
        let dir = host
            .upgrade()
            .ok_or_else(|| fail("хост завершился"))?
            .data_dir
            .join("plugins-data");
        std::fs::create_dir_all(&dir).map_err(fail)?;
        let path = dir.join(format!("{}.json", m.id));
        let store = lua.create_table()?;
        let p = path.clone();
        let stop = stopped.clone();
        store.set(
            "get",
            lua.create_function(move |lua, key: String| {
                if stop.load(Ordering::Relaxed) {
                    return Err(fail("плагин остановлен"));
                }
                let _lock = STORE_LOCK.lock().unwrap();
                let data = read_store(&p)?;
                match data.get(&key) {
                    Some(value) => lua.to_value(value),
                    None => Ok(mlua::Value::Nil),
                }
            })?,
        )?;
        let stop = stopped.clone();
        store.set(
            "set",
            lua.create_function(move |lua, (key, value): (String, mlua::Value)| {
                if stop.load(Ordering::Relaxed) {
                    return Err(fail("плагин остановлен"));
                }
                if key.is_empty() || key.len() > 256 {
                    return Err(fail("ключ хранилища: 1–256 байт"));
                }
                let value: Value = lua.from_value(value)?;
                let _lock = STORE_LOCK.lock().unwrap();
                let mut data = read_store(&path)?;
                if value.is_null() {
                    data.remove(&key);
                } else {
                    data.insert(key, value);
                }
                let bytes = serde_json::to_vec(&data).map_err(fail)?;
                if bytes.len() > 256 * 1024 {
                    return Err(fail("хранилище больше 256 КБ"));
                }
                crate::settings::write_atomic(&path, &bytes, 0o600).map_err(fail)
            })?,
        )?;
        api.set("store", store)?;
    }
    if m.permissions.iter().any(|p| p == "network") {
        let allowed = m.network_allow.clone();
        let stop = stopped.clone();
        let plugin_id = m.id.clone();
        let is_service = super::super::modules::official_lua_service(&m.id)
            && host.upgrade().is_some_and(|h| h.official(&m.id));
        let cached_client: Arc<Mutex<Option<(Value, reqwest::Client)>>> = Arc::new(Mutex::new(None));
        if is_service {
            let host = host.clone();
            let runtime = runtime.clone();
            let plugin_id = plugin_id.clone();
            let cached_client = cached_client.clone();
            api.set(
                "network_check",
                lua.create_function(move |_, ()| {
                    resolve_http_client(&host, &runtime, &plugin_id, true, &cached_client)?;
                    Ok(true)
                })?,
            )?;
        }
        api.set("fetch",lua.create_function(move |lua,(url,options):(String,Option<Table>)|{
            let started=Instant::now();let until=*wall.lock().unwrap();
            let mut url=reqwest::Url::parse(&url).map_err(|_|fail("неверный URL"))?;
            if !permitted(&url,&allowed){return Err(fail("домен не разрешён в network_allow"));}
            let options=options.map(|t|lua.from_value::<Value>(mlua::Value::Table(t))).transpose()?.unwrap_or(json!({}));
            let method=options["method"].as_str().unwrap_or("GET");let method=reqwest::Method::from_bytes(method.as_bytes()).map_err(|_|fail("неверный HTTP метод"))?;
            let req_timeout = options
                .get("timeout_ms")
                .and_then(Value::as_u64)
                .map(|ms| Duration::from_millis(ms.clamp(1000, 25_000)));
            let body_opt=options.get("body").filter(|v|!v.is_null());
            let body_b64_opt=options.get("body_base64").filter(|v|!v.is_null());
            let multipart_opt=options.get("multipart").filter(|v|!v.is_null());
            if [body_opt.is_some(),body_b64_opt.is_some(),multipart_opt.is_some()].into_iter().filter(|&x|x).count()>1{
                return Err(fail("укажите только один тип тела запроса"));
            }
            let mut auto_content_type: Option<String> = None;
            let raw_body: Vec<u8> = if let Some(b) = body_opt {
                let s=b.as_str().ok_or_else(||fail("HTTP body должен быть строкой"))?;
                if s.len()>1024*1024{return Err(fail("HTTP body больше 1 МБ"));}
                s.as_bytes().to_vec()
            } else if let Some(b) = body_b64_opt {
                let s=b.as_str().ok_or_else(||fail("body_base64 должен быть строкой"))?;
                let bytes=B64.decode(s).map_err(|_|fail("неверный body_base64"))?;
                if bytes.len()>3*1024*1024{return Err(fail("body_base64 больше 3 МБ"));}
                bytes
            } else if let Some(parts) = multipart_opt {
                let parts=parts.as_array().ok_or_else(||fail("multipart должен быть массивом"))?;
                let boundary=format!("jarvis-{}",crate::settings::rand_hex(12));
                auto_content_type=Some(format!("multipart/form-data; boundary={boundary}"));
                encode_multipart(&boundary,parts)?
            } else { Vec::new() };
            let resp_enc=options.get("response_encoding").and_then(Value::as_str).unwrap_or("utf8");
            if !matches!(resp_enc,"utf8"|"base64"){return Err(fail("неверный response_encoding"));}
            let headers: BTreeMap<String,String>=options.get("headers").filter(|v|!v.is_null()).map(|v|serde_json::from_value(v.clone())).transpose().map_err(fail)?.unwrap_or_default();
            let client = resolve_http_client(&host, &runtime, &plugin_id, is_service, &cached_client)?;
            let result=runtime.block_on(async {
                let operation=async {
                    for redirect in 0..=5 {
                        if !permitted(&url,&allowed){return Err(fail("переход на запрещённый домен"));}
                        let mut request=client.request(method.clone(),url.clone()).body(raw_body.clone());
                        if let Some(t) = req_timeout {
                            request = request.timeout(t);
                        }
                        if let Some(ct)=&auto_content_type && !headers.keys().any(|k|k.eq_ignore_ascii_case("content-type")){
                            request=request.header("Content-Type",ct);
                        }
                        for (key,value) in &headers {request=request.header(key,value);}
                        let mut response=request.send().await.map_err(|e|fail(crate::net::short(&e)))?;
                        if response.status().is_redirection() {
                            if redirect==5 || !matches!(method,reqwest::Method::GET|reqwest::Method::HEAD){return Err(fail("HTTP redirect отклонён"));}
                            let next=response.headers().get(reqwest::header::LOCATION).and_then(|v|v.to_str().ok()).ok_or_else(||fail("redirect без Location"))?;
                            let next=url.join(next).map_err(fail)?;
                            if url.scheme()=="https" && next.scheme()!="https" {return Err(fail("небезопасный HTTP redirect"));}
                            // Credentials cannot cross origins, even if both hosts are allowed.
                            if next.origin()!=url.origin() && !headers.is_empty(){return Err(fail("redirect с заголовками на другой домен"));}
                            url=next;continue;
                        }
                        let status=response.status().as_u16();let mut bytes=Vec::new();
                        let limit=if resp_enc=="base64" || is_service {4*1024*1024}else{1024*1024};
                        while let Some(chunk)=response.chunk().await.map_err(|e|fail(crate::net::short(&e)))? {
                            if bytes.len()+chunk.len()>limit{return Err(fail(if limit>1024*1024{"HTTP ответ больше 4 МБ"}else{"HTTP ответ больше 1 МБ"}));}bytes.extend_from_slice(&chunk);
                        }
                        if resp_enc=="base64" {
                            return Ok(json!({"status":status,"ok":(200..300).contains(&status),"body_base64":B64.encode(&bytes)}));
                        }
                        return Ok(json!({"status":status,"ok":(200..300).contains(&status),"body":String::from_utf8(bytes).map_err(fail)?}));
                    }
                    Err(fail("слишком много redirect"))
                };
                tokio::select! {
                    value=operation=>value,
                    _=async {loop {if stop.load(Ordering::Relaxed) || Instant::now()>=until {break;}tokio::time::sleep(Duration::from_millis(25)).await;}}=>Err(fail("HTTP прерван или превышено время")),
                }
            });
            *cpu.lock().unwrap()+=started.elapsed();
            lua.to_value(&result?)
        })?)?;
    }
    let timers: Timers = Arc::default();
    if m.permissions.iter().any(|p| p == "timers") {
        let table = lua.create_table()?;
        let next = Arc::new(std::sync::atomic::AtomicU64::new(1));
        for (name, repeating) in [("after", false), ("every", true)] {
            let timers = timers.clone();
            let next = next.clone();
            let stop = stopped.clone();
            table.set(
                name,
                lua.create_function(move |_, (ms, function): (u64, Function)| {
                    if stop.load(Ordering::Relaxed) {
                        return Err(fail("плагин остановлен"));
                    }
                    if ms > 7 * 24 * 3600 * 1000 || (repeating && ms < 100) {
                        return Err(fail("неверный период таймера"));
                    }
                    let mut timers = timers.lock().unwrap();
                    if timers.len() >= 64 {
                        return Err(fail("лимит: 64 таймера"));
                    }
                    let id = next.fetch_add(1, Ordering::Relaxed);
                    let delay = Duration::from_millis(ms);
                    timers.insert(
                        id,
                        Timer {
                            at: Instant::now() + delay,
                            period: repeating.then_some(delay),
                            function,
                        },
                    );
                    Ok(id)
                })?,
            )?;
        }
        let timers = timers.clone();
        table.set(
            "cancel",
            lua.create_function(move |_, id: u64| {
                Ok(timers.lock().unwrap().remove(&id).is_some())
            })?,
        )?;
        api.set("timers", table)?;
    }
    Ok(timers)
}
static STORE_LOCK: Mutex<()> = Mutex::new(());
fn read_store(path: &std::path::Path) -> mlua::Result<Map<String, Value>> {
    let file = match std::fs::File::open(path) {
        Ok(f) => f,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Map::new()),
        Err(e) => return Err(fail(e)),
    };
    let mut bytes = Vec::new();
    file.take(256 * 1024 + 1)
        .read_to_end(&mut bytes)
        .map_err(fail)?;
    if bytes.len() > 256 * 1024 {
        return Err(fail("хранилище больше 256 КБ"));
    }
    serde_json::from_slice(&bytes).map_err(fail)
}
