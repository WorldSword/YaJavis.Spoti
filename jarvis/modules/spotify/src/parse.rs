use serde::{Deserialize, Serialize};

/// Трек в том же виде, что у остальных источников музыки в панели.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Track {
    #[serde(rename = "ID", default)]
    pub id: String,
    #[serde(rename = "Title", default)]
    pub title: String,
    #[serde(rename = "Author", default)]
    pub author: String,
    #[serde(rename = "Art", default, skip_serializing_if = "String::is_empty")]
    pub art: String,
}

/// Что сейчас у Soloist (`soloist ctl now --json`).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Now {
    pub playing: bool,
    pub title: String,
    pub artist: String,
    pub cover: String,
    pub uri: String,
    pub context_uri: String,
    pub context_name: String,
    pub pos: f64,
    pub dur: f64,
    /// Громкость Spotify 0–100; None, если Soloist её не назвал.
    pub volume: Option<u8>,
}

#[derive(Deserialize, Default)]
struct Identity {
    #[serde(default)]
    name: String,
}
#[derive(Deserialize, Default)]
struct Decor {
    #[serde(default)]
    identity: Identity,
    #[serde(default)]
    visual_identity: Visual,
    #[serde(default)]
    creators: Vec<Creator>,
    #[serde(default)]
    playback: Playback,
}
#[derive(Deserialize, Default)]
struct Visual {
    #[serde(default)]
    cover: Vec<Cover>,
}
#[derive(Deserialize, Default)]
struct Cover {
    #[serde(default)]
    url: String,
    #[serde(default)]
    size: String,
}
#[derive(Deserialize, Default)]
struct Creator {
    #[serde(default)]
    entity: Entity,
}
#[derive(Deserialize, Default)]
struct Entity {
    #[serde(default)]
    decorations: Decor0,
}
#[derive(Deserialize, Default)]
struct Decor0 {
    #[serde(default)]
    identity: Identity,
}
#[derive(Deserialize, Default)]
struct Playback {
    #[serde(default)]
    duration_ms: f64,
}
#[derive(Deserialize, Default)]
struct Item {
    #[serde(default)]
    uri: String,
    #[serde(default)]
    decorations: Decor,
}
#[derive(Deserialize, Default)]
struct Context {
    #[serde(default)]
    uri: String,
    #[serde(default)]
    decorations: Decor0,
}
#[derive(Deserialize, Default)]
struct Position {
    #[serde(default)]
    position_ms: f64,
}
#[derive(Deserialize, Default)]
struct RawNow {
    #[serde(default)]
    status: String,
    item: Option<Item>,
    #[serde(default)]
    context: Context,
    #[serde(default)]
    position: Position,
    volume: Option<i64>,
}
#[derive(Deserialize, Default)]
struct RawQueue {
    #[serde(default)]
    upcoming: Vec<Upcoming>,
}
#[derive(Deserialize, Default)]
struct Upcoming {
    #[serde(default)]
    item: Item,
}

/// Вывод команды может идти вместе с журналом Soloist: берём только сам объект.
fn json_slice(s: &str) -> &str {
    match (s.find('{'), s.rfind('}')) {
        (Some(i), Some(j)) if j > i => &s[i..=j],
        _ => s,
    }
}

pub fn is_dj_voice(uri: &str) -> bool {
    uri.starts_with("spotify:media:")
}

fn artists(creators: &[Creator]) -> String {
    creators
        .iter()
        .map(|c| c.entity.decorations.identity.name.as_str())
        .filter(|n| !n.is_empty())
        .collect::<Vec<_>>()
        .join(", ")
}

pub fn parse_now(text: &str) -> Result<Now, String> {
    let raw: RawNow = serde_json::from_str(json_slice(text)).map_err(|e| e.to_string())?;
    let Some(item) = raw.item else {
        return Ok(Now::default());
    };
    let d = item.decorations;
    let mut title = d.identity.name.clone();
    if title.is_empty() && !item.uri.is_empty() {
        title = if is_dj_voice(&item.uri) { "DJ" } else { "Spotify" }.into();
    }
    // «default» — 300 пикселей, хватает для карточки
    let cover = d
        .visual_identity
        .cover
        .iter()
        .find(|c| c.size == "default")
        .or(d.visual_identity.cover.first())
        .map(|c| c.url.clone())
        .unwrap_or_default();
    Ok(Now {
        playing: raw.status == "playing",
        title,
        artist: artists(&d.creators),
        cover,
        uri: item.uri,
        context_uri: raw.context.uri,
        context_name: raw.context.decorations.identity.name,
        pos: raw.position.position_ms / 1000.0,
        dur: d.playback.duration_ms / 1000.0,
        volume: raw.volume.filter(|v| (0..=100).contains(v)).map(|v| v as u8),
    })
}

/// `soloist ctl queue --json`: поле upcoming — следующие треки; озвучка DJ и незнакомое отбрасываются.
pub fn parse_queue(text: &str) -> Result<Vec<Track>, String> {
    let raw: RawQueue = serde_json::from_str(json_slice(text)).map_err(|e| e.to_string())?;
    Ok(raw
        .upcoming
        .into_iter()
        .filter(|u| u.item.uri.starts_with("spotify:track:") || u.item.uri.starts_with("spotify:episode:"))
        .map(|u| {
            let d = u.item.decorations;
            let covers = &d.visual_identity.cover;
            let art = covers
                .iter()
                .find(|c| c.size == "small")
                .or(covers.first())
                .map(|c| c.url.clone())
                .unwrap_or_default();
            Track { id: u.item.uri, title: d.identity.name, author: artists(&d.creators), art }
        })
        .collect())
}

/// Очередь после текущего трека. Сразу после смены трека очередь ещё прежняя и текущий стоит в ней первым:
/// всё до него включительно отбрасываем, чтобы он не мелькал в списке дважды.
pub fn upcoming_after(queue: &[Track], current: &str) -> Vec<Track> {
    let from = if current.is_empty() {
        0
    } else {
        queue.iter().position(|t| t.id == current).map_or(0, |i| i + 1)
    };
    queue[from..].to_vec()
}

/// `spotify:<тип>(:<id>)+`, только латиница и цифры: адрес уходит аргументом в Soloist.
pub fn valid_uri(uri: &str) -> bool {
    let mut parts = uri.split(':');
    if parts.next() != Some("spotify") {
        return false;
    }
    match parts.next() {
        Some(kind) if !kind.is_empty() && kind.bytes().all(|b| b.is_ascii_lowercase()) => {}
        _ => return false,
    }
    let rest: Vec<_> = parts.collect();
    !rest.is_empty()
        && rest
            .iter()
            .all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_'))
}

#[cfg(test)]
mod tests {
    use super::*;
    const NOW: &str = include_str!("../tests/fixtures/soloist_now_dj.json");
    const QUEUE: &str = include_str!("../tests/fixtures/soloist_queue.json");

    #[test]
    fn now_reads_track_fields() {
        let n = parse_now(NOW).unwrap();
        assert!(n.playing);
        assert_eq!(n.title, "SWIM");
        assert_eq!(n.artist, "BTS");
        assert_eq!(n.uri, "spotify:track:68lbSrXDORS51pmyjZv712");
        assert_eq!(n.context_name, "DJ");
        assert_eq!(n.context_uri, "spotify:playlist:37i9dQZF1EYkqdzj48dyYq");
        assert!((n.dur - 150.799).abs() < 1e-6);
        assert!((n.pos - 79.643).abs() < 1e-6);
        assert_eq!(n.volume, Some(50));
        assert!(n.cover.ends_with("dfa17fad7f190c901603270e") && n.cover.contains("00001e02"));
    }

    #[test]
    fn now_skips_log_noise_around_json() {
        let noisy = format!("INFO start\n{NOW}\ntrailing");
        assert_eq!(parse_now(&noisy).unwrap().title, "SWIM");
    }

    #[test]
    fn now_without_item_is_idle() {
        let n = parse_now(r#"{"type":"playback_state","status":"paused"}"#).unwrap();
        assert!(n.uri.is_empty() && !n.playing);
    }

    #[test]
    fn now_names_dj_voice_and_unnamed_items() {
        let dj = r#"{"status":"playing","item":{"uri":"spotify:media:abc"}}"#;
        assert_eq!(parse_now(dj).unwrap().title, "DJ");
        let other = r#"{"status":"playing","item":{"uri":"spotify:track:x"}}"#;
        assert_eq!(parse_now(other).unwrap().title, "Spotify");
        assert_eq!(parse_now(other).unwrap().volume, None);
    }

    #[test]
    fn now_rejects_garbage() {
        assert!(parse_now("not json").is_err());
    }

    #[test]
    fn queue_keeps_only_tracks_and_episodes() {
        let q = parse_queue(QUEUE).unwrap();
        assert!(!q.is_empty());
        assert!(q.iter().all(|t| t.id.starts_with("spotify:track:") || t.id.starts_with("spotify:episode:")));
        assert_eq!(q[0].title, "так похуй");
        assert_eq!(q[0].author, "madk1d");
        assert!(q[0].art.contains("00004851"));
    }

    #[test]
    fn upcoming_drops_current_and_everything_before() {
        let t = |id: &str| Track { id: id.into(), ..Default::default() };
        let q = vec![t("a"), t("b"), t("c")];
        let ids: Vec<_> = upcoming_after(&q, "b").into_iter().map(|t| t.id).collect();
        assert_eq!(ids, ["c"]);
        assert_eq!(upcoming_after(&q, "zzz").len(), 3);
    }

    #[test]
    fn uri_validation() {
        assert!(valid_uri("spotify:track:68lbSrXDORS51pmyjZv712"));
        assert!(valid_uri("spotify:station:track:abc"));
        assert!(!valid_uri("spotify:track"));
        assert!(!valid_uri("spotify:track:a b"));
        assert!(!valid_uri("http://x"));
    }
}
