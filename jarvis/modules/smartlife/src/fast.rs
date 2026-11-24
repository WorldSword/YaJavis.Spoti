use super::{Device, Function};
use jarvis_home_common::normal;

pub fn switches(device: &Device) -> impl Iterator<Item = &Function> {
    device.funcs.iter().filter(|f| {
        f.kind == "Boolean"
            && (matches!(f.code.as_str(), "switch_led" | "switch" | "led_switch")
                || f.code.strip_prefix("switch_").is_some_and(|n| {
                    !n.starts_with('0') && n.len() <= 2 && n.bytes().all(|b| b.is_ascii_digit())
                }))
    })
}

fn light(device: &Device) -> bool {
    matches!(
        device.category.as_str(),
        "dj" | "dd" | "xdd" | "fwd" | "dc" | "tgq" | "tgkg" | "fsd" | "gyd" | "tyndj" | "kg"
    ) || switches(device).any(|f| f.code == "switch_led")
}

fn stems(text: &str) -> Vec<String> {
    normal(text)
        .split_whitespace()
        .filter(|w| {
            !matches!(
                *w,
                "в" | "во"
                    | "на"
                    | "у"
                    | "и"
                    | "пожалуйста"
                    | "джарвис"
                    | "мне"
                    | "там"
                    | "тут"
                    | "здесь"
            )
        })
        .map(|word| {
            let mut chars: Vec<_> = word.chars().collect();
            if chars.len() > 4 {
                chars.pop();
            }
            if chars.len() > 5 && chars.last().is_some_and(|c| "аеиоуыэюяйь".contains(*c))
            {
                chars.pop();
            }
            chars.into_iter().collect()
        })
        .collect()
}

fn same(a: &str, b: &str) -> bool {
    if a == b {
        return true;
    }
    let length = a.chars().count().min(b.chars().count());
    let shared = a.chars().zip(b.chars()).take_while(|(a, b)| a == b).count();
    length >= 4 && (shared == length || length == 4 && shared >= 3)
}

fn subset(a: &[String], b: &[String]) -> bool {
    !a.is_empty() && a.iter().all(|x| b.iter().any(|y| same(x, y)))
}

pub fn pick(text: &str, devices: Vec<Device>, speaker_room: &str) -> Option<(bool, Vec<Device>)> {
    let normalized = normal(text);
    let mut words = normalized
        .split_whitespace()
        .skip_while(|w| matches!(*w, "джарвис" | "пожалуйста"));
    let on = match words.next()? {
        "включи" | "зажги" | "вруби" | "включить" | "вкл" => true,
        "выключи" | "погаси" | "выруби" | "выключить" | "отключи" | "потуши" => {
            false
        }
        _ => return None,
    };
    let rest = words.collect::<Vec<_>>();
    if rest.iter().any(|w| {
        matches!(*w, "ярче" | "тусклее" | "процентов" | "кроме" | "потом")
            || w.chars().any(|c| c.is_ascii_digit())
    }) {
        return None;
    }
    let everywhere = rest
        .iter()
        .any(|w| matches!(*w, "весь" | "везде" | "всем" | "доме"));
    let mut query = stems(&rest.join(" "));
    let named_room = devices
        .iter()
        .map(|d| (d.room.clone(), stems(&d.room)))
        .filter(|(_, w)| subset(w, &query))
        .max_by_key(|(_, w)| w.len());
    let room = named_room
        .as_ref()
        .map(|(r, _)| r.as_str())
        .unwrap_or(speaker_room)
        .to_owned();
    let explicit_room = named_room.is_some() && !everywhere;
    if let Some((_, room_words)) = named_room {
        query.retain(|w| !room_words.iter().any(|r| same(r, w)));
    }
    query.retain(|w| !matches!(w.as_str(), "весь" | "везд" | "всем" | "дом" | "доме"));
    let in_room = |d: &Device| everywhere || room.is_empty() || normal(&d.room) == normal(&room);
    let generic =
        query.len() == 1 && stems("свет освещение люстру люстра лампы").contains(&query[0]);
    let mut picked: Vec<_> = devices
        .into_iter()
        .filter(|d| {
            if switches(d).next().is_none() {
                return false;
            }
            if generic {
                return light(d) && in_room(d);
            }
            if explicit_room && !in_room(d) {
                return false;
            }
            let name = stems(&d.name);
            subset(&query, &name)
        })
        .collect();
    if !generic && picked.len() > 1 && picked.iter().any(&in_room) {
        picked.retain(in_room);
    }
    if picked.is_empty() {
        None
    } else {
        Some((on, picked))
    }
}

#[cfg(test)]
#[test]
fn fast_switches_obey_named_and_speaker_rooms() {
    let device = |name: &str, room: &str, category: &str| Device {
        name: name.into(),
        room: room.into(),
        category: category.into(),
        funcs: vec![Function {
            kind: "Boolean".into(),
            code: "switch_1".into(),
            ..Default::default()
        }],
        ..Default::default()
    };
    let devices = vec![
        device("Лампа", "Зал", "dj"),
        device("Лампа", "Кухня", "dj"),
        device("Компьютер розетка", "Зал", "cz"),
    ];
    let (on, here) = pick("Джарвис, включи свет", devices.clone(), "Зал").unwrap();
    assert!(on);
    assert_eq!(here.len(), 1);
    assert_eq!(here[0].room, "Зал");
    let (on, kitchen) = pick("выключи свет на кухне", devices.clone(), "Зал").unwrap();
    assert!(!on);
    assert_eq!(kitchen.len(), 1);
    assert_eq!(kitchen[0].room, "Кухня");
    assert_eq!(
        pick("выключи весь свет", devices.clone(), "Зал")
            .unwrap()
            .1
            .len(),
        2
    );
    assert_eq!(
        pick("выключи лампу", devices.clone(), "Зал")
            .unwrap()
            .1
            .len(),
        1
    );
    assert!(pick("включи музыку", devices, "Зал").is_none());
}
