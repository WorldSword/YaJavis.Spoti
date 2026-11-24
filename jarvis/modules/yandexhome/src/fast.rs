use super::*;
use model::{Device, actions_for};
use regex_lite::Regex;
use std::sync::LazyLock;
pub(crate) static COMMAND: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^(включи|выключи|погаси|отключи|зажги|запусти сценарий|запусти|останови)\s+(.+?)(?:\s+пожалуйста)?$").unwrap()
});
static COLOR: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^(?:сделай|поставь|включи|переключи|измени|смени)\s+(?:цвет\s+)?(.+?)\s+(?:на\s+)?(красн|оранжев|желт|зелен|голуб|син|фиолетов|розов|бел|тепл|холодн|нейтральн)[а-я]*(?:\s+(?:цвет|свет)[а-я]*)?$").unwrap()
});
static SUFFIX: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^(.+?)\s+(?:в|во|на)\s+(.+)$").unwrap());

pub(crate) fn same_word(a: &str, b: &str) -> bool {
    let (a, b) = (a.chars().collect::<Vec<_>>(), b.chars().collect::<Vec<_>>());
    a == b || (a.len() >= 4 && b.len() >= 4 && a[..a.len() - 1] == b[..b.len() - 1])
}
fn room_match(room: &str, word: &str) -> bool {
    let stem = |s: &str| {
        let c: Vec<_> = s.chars().collect();
        let trim = if c.len() <= 3 {
            0
        } else if c.len() <= 5 {
            1
        } else {
            2
        };
        c[..c.len() - trim].iter().collect::<String>()
    };
    let (room, word) = (norm(room), norm(word));
    let want: Vec<_> = word.split_whitespace().collect();
    let have: Vec<_> = room.split_whitespace().collect();
    !want.is_empty()
        && want.len() == have.len()
        && have
            .iter()
            .zip(want)
            .all(|(a, b)| *a == b || b.starts_with(&stem(a)))
}
fn aliases(kind: &str) -> &'static [&'static str] {
    let k = kind.trim_start_matches("devices.types.");
    if k.starts_with("vacuum_cleaner") {
        &[
            "пылесос",
            "робот пылесос",
            "робот",
            "уборку",
            "уборка",
            "пылик",
        ]
    } else if k.starts_with("thermostat.ac") {
        &["кондиционер", "кондей", "кондер"]
    } else if k.starts_with("humidifier") {
        &["увлажнитель"]
    } else if k.starts_with("purifier") {
        &["очиститель"]
    } else if k.starts_with("media_device.tv") {
        &["телевизор", "телек"]
    } else if k.starts_with("cooking.kettle") {
        &["чайник"]
    } else {
        &[]
    }
}
fn light(d: &Device) -> bool {
    d.cap("on_off", "").is_some()
        && (d.kind.starts_with("devices.types.light")
            || (d.kind == "devices.types.switch"
                && ["свет", "ламп", "люстр", "лент"]
                    .iter()
                    .any(|w| norm(&d.name).contains(w))))
}
pub(crate) fn plan(c: &Catalog, verb: &str, name: &str, speaker_room: &str) -> Option<Args> {
    if verb == "запусти сценарий" {
        let s = c
            .scenarios
            .iter()
            .find(|s| s.is_active != Some(false) && norm(&s.name) == name)?;
        return Some(Args {
            scenario_id: s.id.clone(),
            ..Default::default()
        });
    }
    let on = ["включи", "зажги", "запусти"].contains(&verb);
    let name = name
        .trim_start_matches("устройство ")
        .trim_start_matches("группу ");
    let everywhere = [
        "весь свет",
        "все лампы",
        "все освещение",
        "свет везде",
        "свет во всем доме",
        "свет во всей квартире",
        "везде свет",
    ]
    .contains(&name);
    let (what, scope) = if everywhere {
        (name, None)
    } else if let Some(m) = SUFFIX.captures(name) {
        let scope = m.get(2)?.as_str();
        let ids: Vec<_> = c
            .rooms
            .iter()
            .filter(|r| room_match(&r.name, scope))
            .map(|r| r.id.as_str())
            .collect();
        if ids.len() != 1 {
            return None;
        }
        (m.get(1)?.as_str(), Some(ids))
    } else {
        (name, None)
    };
    // «весь свет» распознаётся до разбора суффикса: «во всём доме» — не имя комнаты.
    if what == "свет" || everywhere {
        let mut room_ids = scope.unwrap_or_default();
        if !everywhere && room_ids.is_empty() {
            if !speaker_room.is_empty() {
                room_ids = c
                    .rooms
                    .iter()
                    .filter(|r| r.id == speaker_room || room_match(&r.name, speaker_room))
                    .map(|r| r.id.as_str())
                    .collect();
            } else {
                room_ids = c
                    .devices
                    .iter()
                    .filter(|d| {
                        d.kind.starts_with("devices.types.smart_speaker") && !d.room.is_empty()
                    })
                    .map(|d| d.room.as_str())
                    .collect();
                room_ids.sort();
                room_ids.dedup();
                if room_ids.len() != 1 {
                    room_ids = c
                        .rooms
                        .iter()
                        .filter(|r| ["зал", "гостиная"].contains(&norm(&r.name).as_str()))
                        .map(|r| r.id.as_str())
                        .collect();
                }
            }
            if room_ids.len() != 1 {
                return None;
            }
        }
        let mut lights: Vec<_> = c
            .devices
            .iter()
            .filter(|d| light(d) && (everywhere || room_ids.contains(&d.room.as_str())))
            .collect();
        if !everywhere {
            let named: Vec<_> = lights
                .iter()
                .copied()
                .filter(|d| norm(&d.name) == "свет")
                .collect();
            if !named.is_empty() {
                lights = named;
            }
        }
        if lights.is_empty() {
            return None;
        }
        return Some(Args {
            device_ids: lights.iter().map(|d| d.id.clone()).collect(),
            on: Some(on),
            ..Default::default()
        });
    }
    let mut exact = vec![];
    let mut partial = vec![];
    let mut check = |id: &str, label: &str, extra: &[&str]| {
        let label = norm(label);
        let mut labels = vec![label.clone()];
        labels.extend(extra.iter().map(|s| norm(s)));
        let mut words: Vec<_> = label.split_whitespace().map(String::from).collect();
        if let Some(first) = words.first_mut() {
            if first.ends_with('а') || first.ends_with('я') {
                let end = if first.ends_with('а') { "у" } else { "ю" };
                first.pop();
                first.push_str(end);
                labels.push(words.join(" "));
            }
        }
        let len = label.split_whitespace().count();
        if labels.iter().any(|s| {
            s == what
                || (s.split_whitespace().count() == what.split_whitespace().count()
                    && s.split_whitespace()
                        .zip(what.split_whitespace())
                        .all(|(a, b)| same_word(a, b)))
        }) {
            exact.push((id.to_string(), len));
        } else if !what.is_empty()
            && what
                .split_whitespace()
                .all(|w| label.split_whitespace().any(|a| same_word(w, a)))
        {
            partial.push((id.to_string(), len));
        }
    };
    for d in &c.devices {
        if d.cap("on_off", "").is_some()
            && scope
                .as_ref()
                .is_none_or(|ids| ids.contains(&d.room.as_str()))
        {
            let mut extra = aliases(&d.kind).to_vec();
            extra.extend(d.aliases.iter().map(String::as_str));
            check(&d.id, &d.name, &extra);
        }
    }
    if scope.is_none() {
        for g in &c.groups {
            check(
                &g.id,
                &g.name,
                &g.aliases.iter().map(String::as_str).collect::<Vec<_>>(),
            );
        }
    }
    let hits = if exact.is_empty() { partial } else { exact };
    let best = hits.iter().map(|(_, n)| *n).min()?;
    let hits: Vec<_> = hits.into_iter().filter(|(_, n)| *n == best).collect();
    if hits.len() != 1 {
        return None;
    }
    Some(Args {
        device_ids: vec![hits[0].0.clone()],
        on: Some(on),
        ..Default::default()
    })
}
fn color_args(stem: &str) -> Args {
    let mut a = Args::default();
    a.color = match stem {
        "красн" => "#FF0000",
        "оранжев" => "#FF7A00",
        "желт" => "#FFD000",
        "зелен" => "#00FF20",
        "голуб" => "#00C8FF",
        "син" => "#0030FF",
        "фиолетов" => "#8A00FF",
        "розов" => "#FF3CA0",
        _ => "",
    }
    .into();
    a.temperature_k = match stem {
        "бел" | "нейтральн" => 4500,
        "тепл" => 2700,
        "холодн" => 6500,
        _ => 0,
    };
    a
}
impl Call<'_> {
    pub(crate) async fn fast(&self, text: &str) -> Option<String> {
        let n = bare(text);
        let c = self.catalog(false).await.ok()?;
        for s in c.scenarios.iter().filter(|s| s.is_active != Some(false)) {
            if s.phrases()
                .into_iter()
                .chain(std::iter::once(s.name.as_str()))
                .any(|ph| !norm(ph).is_empty() && norm(ph) == n)
            {
                return Some(
                    self.apply(&Args {
                        scenario_id: s.id.clone(),
                        ..Default::default()
                    })
                    .await,
                );
            }
        }
        if let Some(m) = COLOR.captures(&n) {
            let p = plan(&c, "включи", m.get(1)?.as_str(), &self.room)?;
            let mut a = color_args(m.get(2)?.as_str());
            a.device_ids = p
                .device_ids
                .into_iter()
                .filter(|id| {
                    c.devices
                        .iter()
                        .any(|d| d.id == *id && !actions_for(d, &a).is_empty())
                })
                .collect();
            if a.device_ids.is_empty() {
                return None;
            }
            return Some(self.apply(&a).await);
        }
        let m = COMMAND.captures(&n)?;
        let a = plan(&c, m.get(1)?.as_str(), m.get(2)?.as_str(), &self.room)?;
        Some(self.apply(&a).await)
    }
}
