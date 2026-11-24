use super::{App, bad, reply};
use axum::{Json, extract::State, http::StatusCode, response::Response};
use jarvis_spotify::{Cmd, Runner, Soloist, parse_now};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{fs, io, path::{Path, PathBuf}, time::Duration};

const FIELDS: [&str; 4] = ["API_KEY", "DEVICE_NAME", "SPOTIFY_CLIENT_ID", "SPOTIFY_CLIENT_SECRET"];

#[derive(Deserialize)]
pub struct Update {
    api_key: Option<String>,
    device_name: Option<String>,
    client_id: Option<String>,
    client_secret: Option<String>,
}

fn soloist() -> Option<Soloist> {
    Soloist::find(std::env::var("SOLOIST_DIR").ok().as_deref())
}

fn yam(root: &Path) -> Option<&Path> {
    let share = root.parent()?;
    (root.file_name()?.to_str()? == "horohi.soloist" && share.file_name()?.to_str()? == "share")
        .then(|| share.parent()).flatten()
}

fn assignment(line: &str) -> Option<(&str, &str)> {
    let line = line.trim();
    let (key, value) = line.strip_prefix("export ").unwrap_or(line).split_once('=')?;
    Some((key.trim(), value.trim()))
}

fn value(text: &str, name: &str) -> String {
    text.lines().filter_map(assignment).filter(|(key, _)| *key == name)
        .last().map(|(_, value)| value.trim_matches(['"', '\'']).to_owned()).unwrap_or_default()
}

fn read(path: &Path) -> io::Result<String> {
    match fs::read_to_string(path) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(String::new()),
        result => result,
    }
}

fn config_dir(root: &Path) -> PathBuf {
    yam(root).map_or_else(|| root.to_owned(), |base| base.join("etc/horohi.soloist"))
}

fn valid(s: &str) -> bool {
    s.len() <= 1024 && !s.chars().any(|c| c.is_control() || matches!(c, '"' | '\'' | '$' | '\\' | '\x60'))
}

fn replace(text: &str, key: &str, value: &str, append: bool) -> String {
    let mut found = false;
    let mut next = String::new();
    for line in text.lines() {
        if assignment(line).is_some_and(|(name, _)| name == key) {
            found = true;
            let indent = &line[..line.len() - line.trim_start().len()];
            let export = if line.trim_start().starts_with("export ") { "export " } else { "" };
            next.push_str(&format!("{indent}{export}{key}={value}\n"));
        } else {
            next.push_str(line);
            next.push('\n');
        }
    }
    if append && !found {
        next.push_str(&format!("{key}={value}\n"));
    }
    next
}

fn write(files: &[(PathBuf, Option<String>, String, u32)]) -> io::Result<()> {
    for (i, (path, _, new, mode)) in files.iter().enumerate() {
        if let Err(error) = crate::settings::write_atomic(path, new.as_bytes(), *mode) {
            for (path, old, _, mode) in files[..i].iter().rev() {
                let result = match old {
                    Some(old) => crate::settings::write_atomic(path, old.as_bytes(), *mode),
                    None => fs::remove_file(path),
                };
                if result.is_err() {
                    log::error!("Spotify: не удалось восстановить {}", path.display());
                }
            }
            return Err(error);
        }
    }
    Ok(())
}

fn save(root: &Path, input: Update) -> io::Result<bool> {
    let dir = config_dir(root);
    let mut files = Vec::new();
    let values = [input.api_key, input.device_name, input.client_id, input.client_secret];
    for (name, range) in [("config.env", 0..2), ("spotify_api.conf", 2..4)] {
        if !range.clone().any(|i| values[i].is_some()) {
            continue;
        }
        let path = dir.join(name);
        let old = read(&path)?;
        let mut next = old.clone();
        for i in range {
            if let Some(v) = &values[i] {
                if value(&old, FIELDS[i]) != *v {
                    next = replace(&next, FIELDS[i], &format!("\"{v}\""), true);
                }
            }
        }
        if next != old {
            files.push((path.clone(), path.exists().then_some(old), next, 0o600));
        }
    }
    if yam(root).is_none() {
        for script in ["start.sh", "scripts/soloist-service.sh"] {
            let path = root.join(script);
            let old = read(&path)?;
            let mut next = old.clone();
            for i in 0..2 {
                // Legacy start.sh may contain literal credentials. Keep shell-derived defaults intact.
                if values[i].is_some() {
                    let config = dir.join("config.env");
                    let escaped = config.to_string_lossy().replace('\'', "'\\''");
                    let field = FIELDS[i];
                    let from_config = format!(
                        r#""$(. '{escaped}'; printf '%s' "${{{field}-}}")""#
                    );
                    next = replace_literal(&next, field, &from_config);
                }
            }
            if next != old {
                files.push((path, Some(old), next, 0o700));
            }
        }
    }
    if !files.is_empty() {
        fs::create_dir_all(dir)?;
        write(&files)?;
    }
    Ok(!files.is_empty())
}

fn assignment_values<'a>(text: &'a str, name: &'a str) -> impl Iterator<Item = &'a str> {
    text.lines().filter_map(assignment).filter(move |(key, _)| *key == name).map(|(_, v)| v)
}

fn replace_literal(text: &str, key: &str, val: &str) -> String {
    let mut next = String::new();
    for line in text.lines() {
        if assignment(line).is_some_and(|(name, old)| name == key && !old.contains('$')) {
            next.push_str(&replace(line, key, val, false));
        } else {
            next.push_str(line);
            next.push('\n');
        }
    }
    next
}

async fn shell(script: &str, args: &[&str], seconds: u64) -> Result<String, String> {
    let mut child = tokio::process::Command::new("sh");
    child.args(["-c", script, "sh"]).args(args)
        .stdin(std::process::Stdio::null()).stderr(std::process::Stdio::null())
        .kill_on_drop(true);
    match tokio::time::timeout(Duration::from_secs(seconds), child.output()).await {
        Ok(Ok(out)) if out.status.success() => Ok(String::from_utf8_lossy(&out.stdout).into_owned()),
        _ => Err("Команда Soloist не завершилась успешно".into()),
    }
}

async fn restart(root: &Path) -> Result<(), String> {
    let (command, script) = if let Some(base) = yam(root) {
        (base.join("bin/soloist"), r#""$1" restart >/dev/null 2>&1"#)
    } else {
        (root.join("bin/soloist-cli"), r#"sh "$1" restart >/dev/null 2>&1"#)
    };
    shell(script, &[command.to_str().ok_or("Некорректный путь Soloist")?], 30).await?;
    Ok(())
}

async fn ctl(soloist: &Soloist, args: &[&str]) -> Result<String, String> {
    soloist.run(Cmd::Ctl(args.iter().map(|s| (*s).to_owned()).collect())).await
}

async fn status(soloist: &Soloist) -> &'static str {
    match ctl(soloist, &["now", "--json"]).await {
        Ok(s) | Err(s) if s.to_ascii_lowercase().contains("not logged in") => "not_logged_in",
        Ok(s) if parse_now(&s).is_ok() => "connected",
        _ => "unavailable",
    }
}

async fn info(soloist: Option<&Soloist>) -> Value {
    let Some(soloist) = soloist else { return json!({"installed":false}); };
    let root = soloist.root();
    let dir = config_dir(root);
    let env = read(&dir.join("config.env")).unwrap_or_default();
    let api = read(&dir.join("spotify_api.conf")).unwrap_or_default();
    let process = shell(r#"
for p in $(pidof soloist); do
    [ -r "/proc/$p/environ" ] || continue
    dev=$(tr '\000' '\n' < "/proc/$p/environ" 2>/dev/null | sed -n 's/^APULSE_PLAYBACK_DEVICE=//p')
    [ -n "$dev" ] && { printf '%s\n' "$dev"; exit 0; }
done
pidof soloist >/dev/null
"#, &[], 3).await;
    let device = process.as_deref().unwrap_or_default().trim();
    json!({
        "installed":true, "yam":yam(root).is_some(), "running":process.is_ok(),
        "auth":status(soloist).await, "audio_device": device,
        "device_name":value(&env, FIELDS[1]),
        "api_key_set":!value(&env, FIELDS[0]).is_empty(),
        "client_id_set":!value(&api, FIELDS[2]).is_empty(),
        "client_secret_set":!value(&api, FIELDS[3]).is_empty()
    })
}

pub async fn get(State(app): State<App>) -> Response {
    if let Ok(_guard) = app.soloist_update.try_lock() {
        reply(StatusCode::OK, info(soloist().as_ref()).await)
    } else {
        bad("Выполняется операция Soloist")
    }
}

pub async fn put(State(app): State<App>, Json(input): Json<Update>) -> Response {
    let Ok(_guard) = app.soloist_update.try_lock() else { return bad("Выполняется операция Soloist"); };
    let Some(soloist) = soloist() else { return bad("Soloist не найден"); };
    for v in [&input.api_key, &input.device_name, &input.client_id, &input.client_secret].into_iter().flatten() {
        if !valid(v) { return bad("Недопустимые символы в настройках Soloist"); }
    }
    let mut error = None;
    match save(soloist.root(), input) {
        Ok(true) => { error = restart(soloist.root()).await.err(); }
        Ok(false) => {}
        Err(_) => return bad("Не удалось сохранить настройки Soloist"),
    }
    let mut state = info(Some(&soloist)).await;
    if error.is_some() { state["warning"] = json!("Настройки сохранены. Не удалось перезапустить Soloist."); }
    reply(StatusCode::OK, state)
}

pub async fn restart_now(State(app): State<App>) -> Response {
    let Ok(_guard) = app.soloist_update.try_lock() else { return bad("Выполняется операция Soloist"); };
    let Some(soloist) = soloist() else { return bad("Soloist не найден"); };
    match restart(soloist.root()).await {
        Ok(()) => reply(StatusCode::OK, info(Some(&soloist)).await),
        Err(error) => bad(error),
    }
}


const AUDIO_SCRIPTS: [&str; 2] = ["start.sh", "scripts/soloist-service.sh"];
const TEST_TRACK: &str = "spotify:track:3DwQ7AH3xGD9h65ezslm6q";

fn audio_files(root: &Path, device: &str) -> Result<Vec<(PathBuf, Option<String>, String, u32)>, String> {
    AUDIO_SCRIPTS.iter().map(|script| {
        let path = root.join(script);
        let old = fs::read_to_string(&path).map_err(|_| format!("Не удалось прочитать {script}"))?;
        if assignment_values(&old, "APULSE_PLAYBACK_DEVICE").next().is_none() {
            return Err(format!("В {script} отсутствует выбор аудиовыхода"));
        }
        let new = replace(&old, "APULSE_PLAYBACK_DEVICE", &format!("\"{device}\""), false);
        Ok((path, Some(old), new, 0o700))
    }).collect()
}

async fn pcm_running() -> bool {
    shell(r#"
for f in /proc/asound/card*/pcm*p/sub*/status; do
    [ -r "$f" ] && grep -q RUNNING "$f" && exit 0
done
exit 1
"#, &[], 2).await.is_ok()
}

async fn check_audio(soloist: &Soloist) -> Result<(), String> {
    if status(soloist).await == "not_logged_in" { return Err("Сначала подключите Soloist к аккаунту Spotify".into()); }
    if let Ok(text) = ctl(soloist, &["now", "--json"]).await {
        if parse_now(&text).is_ok_and(|now| now.playing) {
            return if pcm_running().await { Ok(()) } else { Err("Spotify играет, но ALSA не показывает активный выход".into()) };
        }
    }
    Err("Запустите трек Spotify на колонке и повторите проверку".into())
}

async fn tune(soloist: &Soloist) -> Result<String, String> {
    let root = soloist.root();
    if yam(root).is_some() { return Err("Звуковой выход Soloist, установленного через YAM, настраивается в YAM".into()); }
    if status(soloist).await != "connected" { return Err("Подключите колонку к Spotify перед подбором звука".into()); }
    if pcm_running().await { return Err("Остановите воспроизведение на колонке перед подбором".into()); }

    let mut candidates = vec!["plug:default".to_owned()];
    let cards = fs::read_to_string("/proc/asound/cards").unwrap_or_default();
    for line in cards.lines() {
        if let Some((_, tail)) = line.split_once('[') {
            if let Some((card, _)) = tail.split_once(']') {
                let card = card.trim();
                if !card.is_empty() && card.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_') {
                    let name = format!("sysdefault:CARD={card}");
                    if !candidates.contains(&name) { candidates.push(name); }
                }
            }
        }
    }
    candidates.extend(["default".into(), "dmix_spk_plug".into()]);
    let original = audio_files(root, "plug:default")?;
    let restore: Vec<_> = original.into_iter().map(|(path, old, new, mode)| {
        (path, Some(new), old.unwrap_or_default(), mode)
    }).collect();

    for device in candidates {
        let Ok(files) = audio_files(root, &device) else { break; };
        if write(&files).is_err() { break; }
        if restart(root).await.is_err() { break; }
        tokio::time::sleep(Duration::from_secs(5)).await;
        if status(soloist).await == "not_logged_in" { break; }
        if ctl(soloist, &["play", TEST_TRACK]).await.is_ok() {
            tokio::time::sleep(Duration::from_secs(4)).await;
            let state = info(Some(soloist)).await;
            let passed = state["audio_device"].as_str() == Some(device.as_str()) && check_audio(soloist).await.is_ok();
            let paused = soloist.run(Cmd::Control(vec!["pause".into()])).await.is_ok();
            if passed && paused { return Ok(device); }
        }
    }
    write(&restore).map_err(|_| "Не удалось восстановить исходные настройки аудиовыхода")?;
    restart(root).await.map_err(|_| "Исходные настройки восстановлены. Не удалось перезапустить Soloist")?;
    Err("Рабочий выход не найден. Исходные настройки восстановлены".into())
}

pub async fn check(State(app): State<App>) -> Response {
    let Ok(_guard) = app.soloist_update.try_lock() else { return bad("Выполняется операция Soloist"); };
    let Some(soloist) = soloist() else { return bad("Soloist не найден"); };
    match check_audio(&soloist).await {
        Ok(()) => super::ok(),
        Err(error) => bad(error),
    }
}

pub async fn tune_now(State(app): State<App>) -> Response {
    let Ok(guard) = app.soloist_update.clone().try_lock_owned() else { return bad("Выполняется операция Soloist"); };
    let Some(soloist) = soloist() else { return bad("Soloist не найден"); };
    let result = tokio::spawn(async move {
        let _guard = guard;
        tune(&soloist).await
    }).await;
    match result {
        Ok(Ok(device)) => reply(StatusCode::OK, json!({"audio_device":device})),
        Ok(Err(error)) => bad(error),
        Err(_) => bad("Подбор звука прервался"),
    }
}



#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn save_migrates_legacy_launchers_and_preserves_other_fields() {
        let root = std::env::temp_dir().join(format!("jarvis-soloist-{}", crate::settings::rand_hex(8)));
        fs::create_dir_all(root.join("scripts")).unwrap();
        let start = root.join("start.sh");
        let service = root.join("scripts/soloist-service.sh");
        fs::write(&start, "#!/bin/sh\nAPI_KEY=\"legacy\"\n").unwrap();
        fs::write(&service, "API_KEY=\"\u{24}{API_KEY:-fallback}\"\n").unwrap();
        fs::write(root.join("config.env"), "# keep\nDEVICE_NAME=\"Station\"\n").unwrap();
        let update = || Update {
            api_key: Some("new-key".into()), device_name: None, client_id: None, client_secret: None,
        };
        assert!(save(&root, update()).unwrap());
        let config = fs::read_to_string(root.join("config.env")).unwrap();
        assert_eq!(value(&config, "API_KEY"), "new-key");
        assert_eq!(value(&config, "DEVICE_NAME"), "Station");
        assert!(config.starts_with("# keep\n"));
        let launcher = fs::read_to_string(start).unwrap();
        assert!(launcher.contains("config.env"));
        assert!(!launcher.contains("legacy") && !launcher.contains("new-key"));
        assert!(fs::read_to_string(service).unwrap().contains("fallback"));
        assert!(!save(&root, update()).unwrap());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn rejects_shell_expansions_and_preserves_unicode_names() {
        assert!(valid("Станция 2"));
        assert!(!valid("\u{24}(echo test)"));
        assert!(!valid("line\nbreak"));
    }
}
