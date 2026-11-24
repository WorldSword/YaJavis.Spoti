use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeSet;

pub const FORMAT: &str = "yajarvis-backup";
pub const SCHEMA_VERSION: u32 = 1;
pub const MAX_ZIP: usize = 24 << 20;
pub const MAX_UNPACKED: usize = 48 << 20;
pub const MAX_FILES: usize = 512;
pub const MAX_SINGLE_FILE: usize = 8 << 20;
pub const MANIFEST_NAME: &str = "backup.json";
pub const DATA_PREFIX: &str = "data/";
pub const RESTORE_PLUGINS_FILE: &str = "restore-plugins.json";

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(default)]
pub struct BackupFile {
    pub path: String,
    pub sha256: String,
    pub size: u64,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(default)]
pub struct BackupPlugin {
    pub id: String,
    pub kind: String,
    pub source: String,
    pub official: bool,
    pub version: String,
    pub enabled: bool,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(default)]
pub struct BackupManifest {
    pub format: String,
    pub schema_version: u32,
    pub app_version: String,
    pub created_at: String,
    pub model: String,
    pub arch: String,
    pub files: Vec<BackupFile>,
    pub plugins: Vec<BackupPlugin>,
}

pub fn is_valid_plugin_id(id: &str) -> bool {
    let b = id.as_bytes();
    (2..=32).contains(&b.len())
        && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit())
        && b.iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'-')
}

pub fn is_embedded_voice_id(id: &str) -> bool {
    matches!(id, "chatgpt" | "openai" | "gemini" | "grok" | "elevenlabs")
}

fn clean_rel(rel: &str) -> bool {
    !rel.is_empty()
        && !rel.starts_with('/')
        && !rel.contains('\\')
        && !rel.contains('\0')
        && rel
            .split('/')
            .all(|seg| !seg.is_empty() && seg != "." && seg != "..")
}

fn has_temp_segment(path: &str) -> bool {
    path.split('/')
        .any(|seg| seg.ends_with(".tmp") || seg.ends_with(".old") || seg.ends_with(".write"))
}

pub fn allowed_backup_path(rel: &str) -> bool {
    if !clean_rel(rel) || has_temp_segment(rel) {
        return false;
    }
    if matches!(
        rel,
        "settings.json" | "plugins.json" | "chatgpt-chat.json" | "chatgpt-chat-archive.jsonl"
    ) {
        return true;
    }
    if let Some(rest) = rel.strip_prefix("plugins-data/") {
        if rest.contains('/') {
            return false;
        }
        return rest
            .strip_suffix(".json")
            .is_some_and(|id| is_valid_plugin_id(id) && !is_embedded_voice_id(id));
    }
    if let Some(rest) = rel.strip_prefix("plugins/") {
        let Some((id, sub)) = rest.split_once('/') else {
            return false;
        };
        if !is_valid_plugin_id(id) || is_embedded_voice_id(id) || sub.is_empty() {
            return false;
        }
        if matches!(sub, "plugin.json" | "data.json") {
            return true;
        }
        if sub.ends_with(".lua") {
            return true;
        }
        if let Some(data_sub) = sub.strip_prefix("data/") {
            return !data_sub.is_empty() && !data_sub.ends_with(".wav");
        }
        return false;
    }
    false
}

pub fn is_secret_path(rel: &str) -> bool {
    matches!(rel, "settings.json" | "plugins.json" | RESTORE_PLUGINS_FILE)
        || rel.starts_with("plugins-data/")
        || rel.ends_with("/data.json")
}

pub fn sha256_bytes(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    format!("{:x}", hasher.finalize())
}

pub fn validate_manifest(m: &BackupManifest) -> Result<(), String> {
    if m.format != FORMAT {
        return Err("Этот файл не является бэкапом YaJarvis".into());
    }
    if m.schema_version == 0 {
        return Err("В бэкапе не указана версия формата".into());
    }
    if m.schema_version > SCHEMA_VERSION {
        return Err(format!(
            "Бэкап создан более новой версией Джарвиса (формат v{}, поддерживается до v{}). Сначала обновите Джарвиса.",
            m.schema_version, SCHEMA_VERSION
        ));
    }
    if m.files.len() > MAX_FILES {
        return Err("В бэкапе слишком много файлов".into());
    }
    let mut seen = BTreeSet::new();
    let mut total = 0u64;
    for f in &m.files {
        if !allowed_backup_path(&f.path) {
            return Err(format!("Недопустимый путь в бэкапе: {}", f.path));
        }
        if !seen.insert(f.path.as_str()) {
            return Err(format!("Повторяющийся файл в бэкапе: {}", f.path));
        }
        if f.size > MAX_SINGLE_FILE as u64 {
            return Err(format!("Файл {} в бэкапе слишком большой", f.path));
        }
        if f.sha256.len() != 64 || !f.sha256.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(format!("Неверная контрольная сумма для {}", f.path));
        }
        total = total
            .checked_add(f.size)
            .ok_or_else(|| "Превышен размер распакованного бэкапа".to_string())?;
        if total > MAX_UNPACKED as u64 {
            return Err("Бэкап превышает допустимый размер".into());
        }
    }
    if !seen.contains("settings.json") {
        return Err("В бэкапе отсутствует settings.json".into());
    }
    Ok(())
}

pub fn migrate_settings_json(
    raw: &[u8],
    from_schema: u32,
    _from_app_version: &str,
) -> Result<Vec<u8>, String> {
    if from_schema == 0 || from_schema > SCHEMA_VERSION {
        return Err(format!("Неподдерживаемая версия схемы бэкапа v{from_schema}"));
    }
    let value: serde_json::Value = serde_json::from_slice(raw)
        .map_err(|_| "Файл settings.json в бэкапе повреждён".to_string())?;
    let Some(obj) = value.as_object() else {
        return Err("Файл settings.json должен содержать JSON-объект".into());
    };
    let mut normalized = obj.clone();
    // Schema 1 normalizes null lists only if present; core Store::open handles full field migration.
    for key in ["profiles", "prompts", "eleven_keys", "gemini_keys", "live_voices"] {
        if normalized.get(key).is_some_and(serde_json::Value::is_null) {
            normalized.remove(key);
        }
    }
    serde_json::to_vec_pretty(&serde_json::Value::Object(normalized)).map_err(|e| e.to_string())
}

pub fn migrate_plugins_json(raw: &[u8]) -> Result<Vec<u8>, String> {
    let value: serde_json::Value = serde_json::from_slice(raw)
        .map_err(|_| "Файл plugins.json в бэкапе повреждён".to_string())?;
    let Some(obj) = value.as_object() else {
        return Err("Файл plugins.json должен содержать JSON-объект".into());
    };
    let mut filtered = serde_json::Map::new();
    for (id, entry) in obj {
        if is_valid_plugin_id(id) && !is_embedded_voice_id(id) {
            filtered.insert(id.clone(), entry.clone());
        }
    }
    serde_json::to_vec_pretty(&serde_json::Value::Object(filtered)).map_err(|e| e.to_string())
}

pub fn verify_and_prepare_file(
    entry: &BackupFile,
    raw_file: &[u8],
    manifest: &BackupManifest,
) -> Result<Vec<u8>, String> {
    if raw_file.len() as u64 != entry.size || raw_file.len() > MAX_SINGLE_FILE {
        return Err(format!("Неверный размер файла {}", entry.path));
    }
    if sha256_bytes(raw_file) != entry.sha256 {
        return Err(format!("Файл {} в бэкапе повреждён (SHA-256)", entry.path));
    }
    match entry.path.as_str() {
        "settings.json" => {
            migrate_settings_json(raw_file, manifest.schema_version, &manifest.app_version)
        }
        "plugins.json" => migrate_plugins_json(raw_file),
        "chatgpt-chat.json" => {
            serde_json::from_slice::<serde_json::Value>(raw_file)
                .map_err(|_| "Файл chatgpt-chat.json в бэкапе повреждён".to_string())?;
            Ok(raw_file.to_vec())
        }
        _ => Ok(raw_file.to_vec()),
    }
}

pub fn pending_rust_plugins(manifest: &BackupManifest) -> Vec<BackupPlugin> {
    let mut seen = BTreeSet::new();
    manifest
        .plugins
        .iter()
        .filter(|p| {
            matches!(p.kind.as_str(), "rust" | "module")
                && is_valid_plugin_id(&p.id)
                && !is_embedded_voice_id(&p.id)
                && !p.source.is_empty()
                && p.source != "file"
                && seen.insert(p.id.as_str())
        })
        .cloned()
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn allowlist_accepts_user_state_and_rejects_binaries_and_traversal() {
        for ok in [
            "settings.json",
            "plugins.json",
            "chatgpt-chat.json",
            "chatgpt-chat-archive.jsonl",
            "plugins-data/weather.json",
            "plugins/weather/plugin.json",
            "plugins/weather/main.lua",
            "plugins/youtube/data.json",
            "plugins/youtube/data/cookies.txt",
            "plugins/training/data/wake.bin.tuned",
            "plugins/training/data/wake-profiles/abcdef0123456789abcdef0123456789.bin",
        ] {
            assert!(allowed_backup_path(ok), "should allow {ok}");
        }

        for bad in [
            "",
            "/settings.json",
            "../settings.json",
            "plugins/../etc/passwd",
            "plugins\\weather\\main.lua",
            "jarvisd",
            "jarvis-supervisor",
            "vqe_tap.so",
            "jarvisd.sh",
            "S98jarvisd",
            "seed.json",
            "update-rust.json",
            "wake.bin",
            "plugins/chatgpt/data.json",
            "plugins-data/openai.json",
            "plugins/youtube/youtube",
            "plugins/training/data/rec_001.wav",
            "plugins/training/data.tmp/x.json",
        ] {
            assert!(!allowed_backup_path(bad), "should reject {bad}");
        }
    }

    #[test]
    fn validates_manifest_and_rejects_future_schemas() {
        let valid_hash = "a".repeat(64);
        let mut m = BackupManifest {
            format: FORMAT.into(),
            schema_version: SCHEMA_VERSION,
            app_version: "0.10.15".into(),
            created_at: "2026-11-22T12:00:00Z".into(),
            model: "lite".into(),
            arch: "arm64".into(),
            files: vec![BackupFile {
                path: "settings.json".into(),
                sha256: valid_hash.clone(),
                size: 120,
            }],
            plugins: vec![
                BackupPlugin {
                    id: "vpn".into(),
                    kind: "rust".into(),
                    source: "newfpv/YaJarvis".into(),
                    official: true,
                    version: "0.3.4".into(),
                    enabled: true,
                },
                BackupPlugin {
                    id: "chatgpt".into(),
                    kind: "rust".into(),
                    source: "core".into(),
                    official: true,
                    version: "0.2.5".into(),
                    enabled: true,
                },
                BackupPlugin {
                    id: "weather".into(),
                    kind: "lua".into(),
                    source: "newfpv/YaJarvis".into(),
                    official: true,
                    version: "1.1.0".into(),
                    enabled: true,
                },
            ],
        };
        validate_manifest(&m).unwrap();
        let pending = pending_rust_plugins(&m);
        assert_eq!(pending.len(), 1);
        assert_eq!(pending[0].id, "vpn");

        m.schema_version = SCHEMA_VERSION + 1;
        let err = validate_manifest(&m).unwrap_err();
        assert!(err.contains("более новой версией"));
    }

    #[test]
    fn migrates_settings_and_strips_embedded_plugins_state() {
        let migrated = migrate_settings_json(
            br#"{"profiles":null,"active":"default","custom_extra":42}"#,
            1,
            "0.9.68",
        )
        .unwrap();
        let parsed: serde_json::Value = serde_json::from_slice(&migrated).unwrap();
        assert!(parsed.get("profiles").is_none());
        assert_eq!(parsed["custom_extra"], 42);

        let plugins = migrate_plugins_json(
            br#"{"vpn":{"enabled":true},"chatgpt":{"enabled":true},"weather":{"enabled":true}}"#,
        )
        .unwrap();
        let plugins: serde_json::Value = serde_json::from_slice(&plugins).unwrap();
        assert!(plugins.get("chatgpt").is_none());
        assert_eq!(plugins["vpn"]["enabled"], true);
        assert_eq!(plugins["weather"]["enabled"], true);
    }
}
