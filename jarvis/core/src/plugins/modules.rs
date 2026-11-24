//! IDs reserved for official packages and manifests of optional developer extensions.
use super::Manifest;

pub const EMBEDDED_VOICE_IDS: &[&str] = &["chatgpt", "openai", "gemini", "grok", "elevenlabs"];

pub const OFFICIAL_RUST_IDS: &[&str] = &[
    "homeassistant", "smartlife", "xiaomi", "vpn", "youtube", "spotify", "training",
];

pub const OFFICIAL_LUA_SERVICE_IDS: &[&str] = &["yandexhome"];

pub const OFFICIAL_IDS: &[&str] = &[
    "chatgpt", "openai", "gemini", "grok", "elevenlabs", "homeassistant",
    "yandexhome", "smartlife", "xiaomi", "vpn", "youtube", "spotify", "training",
];

pub fn built_in(id: &str) -> bool {
    OFFICIAL_IDS.contains(&id)
}

pub fn is_embedded(id: &str) -> bool {
    EMBEDDED_VOICE_IDS.contains(&id)
}

pub fn official_rust(id: &str) -> bool {
    OFFICIAL_RUST_IDS.contains(&id)
}

pub fn official_lua_service(id: &str) -> bool {
    OFFICIAL_LUA_SERVICE_IDS.contains(&id)
}

pub fn catalog() -> impl Iterator<Item = (Manifest, bool)> {
    #[cfg(feature = "native-plugins")]
    return super::native_addon::entries().map(|(manifest, _)| (manifest, true));
    #[cfg(not(feature = "native-plugins"))]
    std::iter::empty()
}

pub fn manifests() -> Vec<Manifest> {
    catalog().filter_map(|(m, compiled)| compiled.then_some(m)).collect()
}

pub fn archive(id: &str) -> Result<Vec<u8>, String> {
    use std::io::Write;
    let m = manifests()
        .into_iter()
        .find(|m| m.id == id)
        .ok_or("Этот модуль не включён в сборку")?;
    let mut zip = zip::ZipWriter::new(std::io::Cursor::new(Vec::new()));
    zip.start_file("plugin.json", zip::write::SimpleFileOptions::default())
        .map_err(|e| e.to_string())?;
    zip.write_all(&serde_json::to_vec_pretty(&m).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())?;
    Ok(zip.finish().map_err(|e| e.to_string())?.into_inner())
}
