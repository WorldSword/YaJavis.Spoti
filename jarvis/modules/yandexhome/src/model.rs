use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::collections::BTreeMap;

#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Catalog {
    #[serde(deserialize_with = "nd")]
    pub rooms: Vec<Named>,
    #[serde(deserialize_with = "nd")]
    pub devices: Vec<Device>,
    #[serde(deserialize_with = "nd")]
    pub groups: Vec<Group>,
    #[serde(deserialize_with = "nd")]
    pub scenarios: Vec<Scenario>,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Named {
    pub id: String,
    pub name: String,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Group {
    pub id: String,
    pub name: String,
    #[serde(deserialize_with = "nd")]
    pub devices: Vec<String>,
    #[serde(deserialize_with = "nd")]
    pub aliases: Vec<String>,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Device {
    pub id: String,
    pub name: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(deserialize_with = "nd")]
    pub room: String,
    #[serde(deserialize_with = "nd")]
    pub aliases: Vec<String>,
    #[serde(deserialize_with = "nd")]
    pub capabilities: Vec<Capability>,
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Capability {
    #[serde(rename = "type")]
    pub kind: String,
    pub parameters: Value,
    pub state: Value,
}
impl Device {
    pub fn cap(&self, kind: &str, instance: &str) -> Option<&Capability> {
        self.capabilities.iter().find(|c| {
            c.kind == format!("devices.capabilities.{kind}")
                && (instance.is_empty() || c.parameters["instance"] == instance)
        })
    }
    pub fn describe(&self) -> String {
        let mut parts = vec![];
        for c in &self.capabilities {
            let p = &c.parameters;
            let instance = p["instance"].as_str().unwrap_or("");
            match c.kind.trim_start_matches("devices.capabilities.") {
                "on_off" => parts.push(format!(
                    "вкл/выкл{}",
                    match c.state["value"].as_bool() {
                        Some(true) => " (сейчас вкл)",
                        Some(false) => " (сейчас выкл)",
                        _ => "",
                    }
                )),
                "range" => {
                    if let (Some(min), Some(max)) =
                        (p["range"]["min"].as_f64(), p["range"]["max"].as_f64())
                    {
                        parts.push(format!("{instance} {min}..{max}"));
                    }
                }
                "color_setting" => {
                    if p["color_model"].as_str().is_some_and(|s| !s.is_empty()) {
                        parts.push("цвет".into());
                    }
                    if let (Some(min), Some(max)) = (
                        p["temperature_k"]["min"].as_i64(),
                        p["temperature_k"]["max"].as_i64(),
                    ) {
                        if max > min {
                            parts.push(format!("белый {min}..{max}K"));
                        }
                    }
                    if let Some(s) = p["color_scene"]["scenes"].as_array() {
                        parts.push(format!(
                            "сцены {}",
                            s.iter()
                                .filter_map(|v| v["id"].as_str())
                                .collect::<Vec<_>>()
                                .join(",")
                        ));
                    }
                }
                "mode" => parts.push(format!(
                    "режим {instance} [{}]",
                    p["modes"]
                        .as_array()
                        .into_iter()
                        .flatten()
                        .filter_map(|v| v["value"].as_str())
                        .collect::<Vec<_>>()
                        .join(",")
                )),
                "toggle" => parts.push(format!("переключатель {instance}")),
                _ => {}
            }
        }
        parts.join("; ")
    }
}
#[derive(Clone, Default, Deserialize)]
#[serde(default)]
pub struct Scenario {
    pub id: String,
    pub name: String,
    pub is_active: Option<bool>,
    #[serde(deserialize_with = "nd")]
    pub triggers: Vec<Value>,
}
impl Scenario {
    pub fn phrases(&self) -> Vec<&str> {
        self.triggers
            .iter()
            .filter(|t| t["trigger"]["type"] == "scenario.trigger.voice")
            .filter_map(|t| t["trigger"]["value"].as_str())
            .filter(|s| !s.is_empty())
            .collect()
    }
}

#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct Args {
    #[serde(deserialize_with = "nd")]
    pub device_ids: Vec<String>,
    pub on: Option<bool>,
    pub brightness: i64,
    pub color: String,
    pub temperature_k: i64,
    #[serde(deserialize_with = "nd")]
    pub modes: BTreeMap<String, String>,
    #[serde(deserialize_with = "nd")]
    pub toggles: BTreeMap<String, bool>,
    #[serde(deserialize_with = "nd")]
    pub ranges: BTreeMap<String, f64>,
    pub scenario_id: String,
}
fn nd<'de, D: serde::Deserializer<'de>, T: Default + Deserialize<'de>>(
    d: D,
) -> Result<T, D::Error> {
    Option::<T>::deserialize(d).map(Option::unwrap_or_default)
}
pub fn hex_rgb(s: &str) -> Option<u32> {
    let s = s.trim().trim_start_matches('#');
    if s.len() != 6 || !s.is_ascii() {
        return None;
    }
    u32::from_str_radix(s, 16).ok()
}
pub fn rgb_hsv(rgb: u32) -> Value {
    let (r, g, b) = (
        ((rgb >> 16) & 255) as f64 / 255.0,
        ((rgb >> 8) & 255) as f64 / 255.0,
        (rgb & 255) as f64 / 255.0,
    );
    let max = r.max(g).max(b);
    let min = r.min(g).min(b);
    let d = max - min;
    let h = if d == 0.0 {
        0.0
    } else if max == r {
        60.0 * ((g - b) / d).rem_euclid(6.0)
    } else if max == g {
        60.0 * ((b - r) / d + 2.0)
    } else {
        60.0 * ((r - g) / d + 4.0)
    };
    json!({"h":h.round() as i64,"s":(if max==0.0 {0.0}else{d/max}*100.0).round()as i64,"v":(max*100.0).round()as i64})
}
fn bounded(value: f64, p: &Value) -> f64 {
    if let (Some(min), Some(max)) = (p["min"].as_f64(), p["max"].as_f64()) {
        if min.is_finite() && max.is_finite() && min <= max {
            return value.clamp(min, max);
        }
    }
    value
}
pub fn actions_for(d: &Device, a: &Args) -> Vec<Value> {
    let mut actions = vec![];
    let mut add = |kind: &str, instance: &str, value: Value| {
        actions.push(json!({"type":format!("devices.capabilities.{kind}"),"state":{"instance":instance,"value":value}}))
    };
    if let (Some(c), Some(rgb)) = (d.cap("color_setting", ""), hex_rgb(&a.color)) {
        match c.parameters["color_model"].as_str() {
            Some("hsv") => add("color_setting", "hsv", rgb_hsv(rgb)),
            Some("rgb") => add("color_setting", "rgb", rgb.into()),
            _ => {}
        }
    }
    if a.temperature_k > 0 {
        if let Some(c) = d.cap("color_setting", "") {
            if c.parameters["temperature_k"].is_object() {
                add(
                    "color_setting",
                    "temperature_k",
                    (bounded(a.temperature_k as f64, &c.parameters["temperature_k"]) as i64).into(),
                );
            }
        }
    }
    if a.brightness > 0 {
        if let Some(c) = d.cap("range", "brightness") {
            add(
                "range",
                "brightness",
                bounded(a.brightness.min(100) as f64, &c.parameters["range"]).into(),
            );
        }
    }
    for (instance, value) in &a.ranges {
        if instance == "brightness" && a.brightness > 0 {
            continue;
        }
        if value.is_finite() {
            if let Some(c) = d.cap("range", instance) {
                add(
                    "range",
                    instance,
                    bounded(*value, &c.parameters["range"]).into(),
                );
            }
        }
    }
    for (instance, value) in &a.modes {
        if let Some(c) = d.cap("mode", instance) {
            if c.parameters["modes"]
                .as_array()
                .into_iter()
                .flatten()
                .any(|m| m["value"] == *value)
            {
                add("mode", instance, value.clone().into());
            }
        }
    }
    for (instance, value) in &a.toggles {
        if d.cap("toggle", instance).is_some() {
            add("toggle", instance, (*value).into());
        }
    }
    let on = a.on.or_else(|| {
        ((!a.color.is_empty() || a.temperature_k > 0 || a.brightness > 0) && !actions.is_empty())
            .then_some(true)
    });
    if let Some(on) = on {
        if d.cap("on_off", "").is_some() {
            actions.insert(
                0,
                json!({"type":"devices.capabilities.on_off","state":{"instance":"on","value":on}}),
            );
        }
    }
    actions
}
