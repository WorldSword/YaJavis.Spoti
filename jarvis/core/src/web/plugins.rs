//! Плагины в панели: список, каталог, установка, включение, настройки и удаление.

use super::{App, bad, ok, reply};
use crate::plugins::{Manifest, PLUGIN_API, arch, catalog::{TOPIC, valid_repo}, categories_json, install::{INSTALL_DEFERRED, InstallRequest, Installation, boot_id, free_mb}, newer};
use axum::{
    Json,
    body::Bytes,
    extract::{Path, Query, State},
    http::StatusCode,
    response::Response,
};
use serde_json::{Map, Value, json};
use std::collections::HashMap;
use tokio::sync::{Mutex, MutexGuard};

static PACKAGE_CHANGE: Mutex<()> = Mutex::const_new(());

fn package_busy() -> Response {
    bad("Подождите завершения изменений плагинов на колонке")
}

pub async fn list(State(app): State<App>) -> Json<Value> {
    Json(json!({
        "installed": app.plugins.list(), "composition_pending": app.data_dir.join(jarvis_station::PENDING).exists(),
        "modules": crate::plugins::compiled_modules(), "arch": arch(), "api": PLUGIN_API,
        "free_mb": free_mb(&app.data_dir), "categories": categories_json(), "warned": app.store.with(|s| s.plugins_warned),
        "topic": TOPIC, "repo": crate::DEFAULT_REPO, "boot_id": boot_id(),
    }))
}

pub async fn catalog(State(app): State<App>, Query(q): Query<HashMap<String, String>>) -> Json<Value> {
    let fresh = q.get("fresh").is_some_and(|v| v == "1");
    let (items, err) = match q.get("scope").map(String::as_str) {
        Some("official") => app.plugins.official_catalog(fresh).await,
        Some("community") => app.plugins.community_catalog(fresh).await,
        _ => {
            let ((mut official, official_error), (community, community_error)) = tokio::join!(
                app.plugins.official_catalog(fresh),
                app.plugins.community_catalog(fresh),
            );
            official.extend(community);
            let error = [official_error, community_error].into_iter()
                .filter(|error| !error.is_empty()).collect::<Vec<_>>().join("; ");
            (official, error)
        }
    };
    Json(json!({"items": items, "error": err}))
}

fn field<'a>(v: &'a Value, k: &str) -> Option<&'a Value> {
    v.as_object()?.iter().find(|(key, _)| key.eq_ignore_ascii_case(k)).map(|(_, v)| v)
}

pub async fn install(State(app): State<App>, body: Bytes) -> Response {
    let Ok(operation) = PACKAGE_CHANGE.try_lock() else { return package_busy(); };
    let Ok(req) = serde_json::from_slice::<InstallRequest>(&body) else { return bad("Не указан плагин") };
    let repo = if req.repo.is_empty() { crate::DEFAULT_REPO } else { &req.repo };
    if repo.eq_ignore_ascii_case(crate::DEFAULT_REPO) {
        if crate::plugins::compiled_modules().iter().any(|module| module == &req.id) && !req.update {
            return match app.plugins.install_module(&req.id).await {Ok(m)=>reply(StatusCode::OK,json!({"ok":true,"plugin":m})),Err(e)=>bad(e)};
        }
    }
    match app.plugins.install_repo(&req, || true).await {
        Ok(result) => installed(app, result, operation),
        Err(e) => bad(e),
    }
}

pub async fn upload(State(app): State<App>, body: Bytes) -> Response {
    let Ok(operation) = PACKAGE_CHANGE.try_lock() else { return package_busy(); };
    match app.plugins.install(&body, "file", false).await {
        Ok(result) => installed(app, result, operation),
        Err(e) => bad(e),
    }
}

fn installed(app: App, result: Installation, operation: MutexGuard<'static, ()>) -> Response {
    if result.reboot_from.is_some() {
        schedule_reboot(app, vec![result.manifest.id.clone()], operation, false);
    }
    reply(StatusCode::OK, json!({"ok": true, "plugin": result.manifest, "changed": result.changed, "reboot_from": result.reboot_from}))
}

pub async fn update_all(State(app): State<App>, Json(mut requests): Json<Vec<InstallRequest>>) -> Response {
    let Ok(operation) = PACKAGE_CHANGE.try_lock() else { return package_busy(); };
    if requests.len() > 64 {
        return bad("Слишком много плагинов в одном обновлении");
    }
    let batch = install_batch(&app, &mut requests, false).await;
    if !batch.reboot_ids.is_empty() {
        schedule_reboot(app, batch.reboot_ids, operation, false);
    }
    reply(StatusCode::OK, json!({"ok": true, "updated": batch.updated, "errors": batch.errors, "reboot_from": batch.reboot_from}))
}

#[derive(Default)]
struct Batch {
    updated: Vec<Manifest>,
    errors: Vec<Value>,
    reboot_from: Option<String>,
    reboot_ids: Vec<String>,
    deferred: bool,
}

pub(super) fn idle(app: &App) -> bool {
    !app.engine.talking() && !app.engine.music.playing()
}

async fn install_batch(app: &App, requests: &mut [InstallRequest], automatic: bool) -> Batch {
    let can_apply = || !automatic || (idle(app) && app.store.with(|s| s.update_check && s.update_auto && (3..6).contains(&s.now().hour())));
    let mut batch = Batch::default();
    for request in requests {
        if !can_apply() {
            batch.deferred = true;
            break;
        }
        request.update = true;
        match app.plugins.install_repo(request, can_apply).await {
            Ok(result) if result.changed => {
                if let Some(boot_id) = result.reboot_from {
                    batch.reboot_from = Some(boot_id);
                    batch.reboot_ids.push(result.manifest.id.clone());
                }
                batch.updated.push(result.manifest);
            }
            Ok(_) => {},
            Err(error) if error == INSTALL_DEFERRED => {
                batch.deferred = true;
                break;
            }
            Err(error) => batch.errors.push(json!({"id": request.id, "error": error})),
        }
    }
    batch
}

fn automatic_requests(installed: &[Value], catalog: &[Value]) -> Vec<InstallRequest> {
    installed.iter().filter_map(|plugin| {
        let id = plugin["id"].as_str()?;
        let repo = plugin["source"].as_str()?;
        if !valid_repo(repo) || !matches!(plugin["kind"].as_str(), Some("lua" | "rust")) {
            return None;
        }
        let expected_version = if plugin["official"] == true {
            let available = catalog.iter().find(|item| item["id"] == id)?["version"].as_str()?;
            if !newer(available, plugin["version"].as_str().unwrap_or("")) {
                return None;
            }
            available.to_owned()
        } else {
            String::new()
        };
        Some(InstallRequest { id: id.into(), repo: repo.into(), expected_version, update: true })
    }).collect()
}

pub(super) async fn automatic(app: &App) -> bool {
    let Ok(operation) = PACKAGE_CHANGE.try_lock() else { return false; };
    let installed = app.plugins.list();
    let (catalog, error) = if installed.iter().any(|p| p["official"] == true) {
        app.plugins.official_catalog(false).await
    } else {
        (Vec::new(), String::new())
    };
    let mut requests = automatic_requests(&installed, &catalog);
    let batch = install_batch(app, &mut requests, true).await;
    for failure in &batch.errors {
        log::warn!("плагины: автообновление {}: {}", failure["id"], failure["error"]);
    }
    if !batch.reboot_ids.is_empty() {
        schedule_reboot(app.clone(), batch.reboot_ids, operation, true);
    }
    !batch.deferred && error.is_empty()
}

fn schedule_reboot(app: App, plugins: Vec<String>, operation: MutexGuard<'static, ()>, automatic: bool) {
    tokio::spawn(async move {
        let _operation = operation;
        // Дать панели получить ответ до отключения сети.
        tokio::time::sleep(std::time::Duration::from_secs(2)).await;
        while automatic && !idle(&app) {
            tokio::time::sleep(std::time::Duration::from_secs(5)).await;
        }
        log::info!("плагины: перезагрузка после обновления {}", plugins.join(", "));
        match tokio::process::Command::new("/sbin/reboot").status().await {
            Ok(status) if status.success() => {
                tokio::time::sleep(std::time::Duration::from_secs(20)).await;
                log::error!("плагины: колонка не отключилась после команды перезагрузки");
            }
            result => log::error!("плагины: перезагрузка не запустилась: {result:?}"),
        }
        for id in plugins {
            app.plugins.start(&id).await;
        }
    });
}

pub async fn enable(State(app): State<App>, Path(id): Path<String>, body: Bytes) -> Response {
    let Ok(_operation) = PACKAGE_CHANGE.try_lock() else { return package_busy(); };
    let req: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
    let on = field(&req, "on").and_then(Value::as_bool).unwrap_or(false);
    match app.plugins.set_enabled(&id, on).await {
        Ok(()) => {
            if id == "training" && !on { app.engine.set_background_training(None); }
            ok()
        },
        Err(e) => bad(e),
    }
}

pub async fn settings(State(app): State<App>, Path(id): Path<String>, body: Bytes) -> Response {
    let Ok(req) = serde_json::from_slice::<Map<String, Value>>(&body) else { return bad("настройки не JSON") };
    match app.plugins.set_settings(&id, &req) {
        Ok(()) => ok(),
        Err(e) => bad(e),
    }
}

pub async fn remove(State(app): State<App>, Path(id): Path<String>) -> Response {
    let Ok(_operation) = PACKAGE_CHANGE.try_lock() else { return package_busy(); };
    match app.plugins.uninstall(&id).await {
        Ok(()) => {
            if id == "training" { app.engine.set_background_training(None); }
            app.updater.invalidate();
            ok()
        },
        Err(e) => bad(e),
    }
}

pub async fn warned(State(app): State<App>) -> Response {
    let _ = app.store.update(|s| {
        s.plugins_warned = true;
        Ok::<(), std::io::Error>(())
    });
    ok()
}

/// Мастер знакомства пройден (или пропущен); ?again=1 — показать снова.
pub async fn onboarded(State(app): State<App>, Query(q): Query<HashMap<String, String>>) -> Response {
    let done = q.get("again").is_none_or(|v| v != "1");
    let _ = app.store.update(|s| {
        s.onboarded = done;
        Ok::<(), std::io::Error>(())
    });
    ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nightly_updates_select_only_installed_plugins_with_tracked_packages() {
        let repo = crate::DEFAULT_REPO;
        let installed = [
            json!({"id":"chatgpt","official":true,"source":repo,"kind":"rust","version":"0.2.4"}),
            json!({"id":"gemini","official":true,"source":repo,"kind":"rust","version":"1.1.0"}),
            json!({"id":"weather","official":true,"source":repo,"kind":"lua","version":"1.0.0"}),
            json!({"id":"installed-file","source":"file","kind":"lua","version":"1.0.0"}),
            json!({"id":"tracked","source":"author/extension","kind":"lua","version":"1.0.0"}),
        ];
        let catalog = [
            json!({"id":"chatgpt","version":"0.2.5"}),
            json!({"id":"gemini","version":"1.0.0"}),
            json!({"id":"weather","version":"1.0.0"}),
            json!({"id":"new-plugin","version":"1.0.0"}),
        ];
        let requests = automatic_requests(&installed, &catalog);
        let updates: Vec<_> = requests.iter().map(|request| (request.id.as_str(), request.expected_version.as_str())).collect();
        assert_eq!(updates, [("chatgpt", "0.2.5"), ("tracked", "")]);
    }
}
