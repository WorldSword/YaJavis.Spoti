//! Prepare a copy of Quasar's current launch profile on every boot.
//! The init script bind-mounts that copy; the original system file stays intact.
use serde_json::{Value, json};
use std::path::Path;

fn rewrite(value: &mut Value, lib: &str, socket: &str, channel: Option<&str>) -> usize {
    match value {
        Value::Object(object) => {
            let target = object.get("path").and_then(Value::as_str).is_some_and(|s| s.ends_with("/maind"))
                && object.get("arg").and_then(Value::as_array).is_some_and(|a| a.iter().any(|v| v == "--platformd"));
            if target {
                let mut env = Vec::new();
                let mut preload = lib.to_string();
                for entry in object.get("envs").and_then(Value::as_array).into_iter().flatten() {
                    let text = entry.as_str().unwrap_or("");
                    if let Some(paths) = text.strip_prefix("LD_PRELOAD=") {
                        for path in paths.split(':').filter(|p| !p.is_empty() && !p.ends_with("/vqe_tap.so")) {
                            preload.push(':'); preload.push_str(path);
                        }
                    } else if !["JARVIS_MIC_SOCKET=", "ALICE_CAPTURE_SOCKET=", "JARVIS_MIC_CHANNEL="].iter().any(|p| text.starts_with(p)) {
                        env.push(entry.clone());
                    }
                }
                env.push(json!(format!("LD_PRELOAD={preload}")));
                env.push(json!(format!("JARVIS_MIC_SOCKET={socket}")));
                if let Some(channel) = channel { env.push(json!(format!("JARVIS_MIC_CHANNEL={channel}"))); }
                object.insert("envs".into(), Value::Array(env));
            }
            usize::from(target) + object.values_mut().map(|v| rewrite(v, lib, socket, channel)).sum::<usize>()
        }
        Value::Array(array) => array.iter_mut().map(|v| rewrite(v, lib, socket, channel)).sum(),
        _ => 0,
    }
}

pub fn patch(input: &str, output: &str, lib: &str, socket: &str) -> Result<(), String> {
    let bytes = std::fs::read(input).map_err(|e| e.to_string())?;
    let mut value: Value = serde_json::from_slice(&bytes).map_err(|_| "launch.json не JSON")?;
    if !value.is_object() { return Err("launch.json должен быть объектом".into()); }
    let channel = std::fs::read_to_string(Path::new(lib).parent().unwrap_or(Path::new(".")).join("mic-channel")).unwrap_or_default();
    let channel = channel.trim();
    let valid = regex_lite::Regex::new(r"^[0-9]{1,2}:[0-9]$").unwrap().is_match(channel);
    if rewrite(&mut value, lib, socket, valid.then_some(channel)) == 0 {
        return Err("в launch.json нет процесса maind --platformd; микрофон на этой прошивке не подключить".into());
    }
    crate::settings::write_atomic(Path::new(output), &serde_json::to_vec_pretty(&value).map_err(|e| e.to_string())?, 0o644).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn replaces_only_platform_tap_and_is_idempotent() {
        let mut doc = json!({"nodes":[{"tasks":[
            {"path":"/system/vendor/quasar/maind","arg":["--mediad"],"envs":["A=1"]},
            {"path":"/system/vendor/quasar/maind","arg":["--platformd"],"envs":["MALLOC_ARENA_MAX=2","LD_PRELOAD=/old/vqe_tap.so:/x/other.so","ALICE_CAPTURE_SOCKET=/tmp/old","JARVIS_MIC_CHANNEL=7:0"]}
        ]}]});
        assert_eq!(rewrite(&mut doc, "/new/vqe_tap.so", "/tmp/mic.sock", Some("6:0")), 1);
        let expected = doc.clone();
        assert_eq!(rewrite(&mut doc, "/new/vqe_tap.so", "/tmp/mic.sock", Some("6:0")), 1);
        assert_eq!(doc, expected);
        assert_eq!(doc["nodes"][0]["tasks"][0]["envs"], json!(["A=1"]));
        assert_eq!(doc["nodes"][0]["tasks"][1]["envs"], json!(["MALLOC_ARENA_MAX=2","LD_PRELOAD=/new/vqe_tap.so:/x/other.so","JARVIS_MIC_SOCKET=/tmp/mic.sock","JARVIS_MIC_CHANNEL=6:0"]));
        assert_eq!(rewrite(&mut json!({}), "x", "y", None), 0);
    }
}
