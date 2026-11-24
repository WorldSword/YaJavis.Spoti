//! Playback state reported by the station's own music player.
use serde::Deserialize;
use serde_json::{Value, json};
use std::time::{Duration, Instant};

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Track {
    #[serde(default)]
    title: String,
    #[serde(default)]
    duration_ms: u64,
    #[serde(default)]
    cover_uri: String,
    #[serde(default)]
    artists: Vec<Name>,
    #[serde(default)]
    albums: Vec<Album>,
}

#[derive(Deserialize)]
struct Name {
    name: String,
}

#[derive(Deserialize)]
struct Album {
    title: String,
}

#[derive(Clone, Copy)]
enum Playback {
    Playing(Instant),
    Paused(Instant),
}

#[derive(Default)]
pub struct AliceMusic {
    id: String,
    track: Track,
    pos: f64,
    playback: Option<Playback>,
}

fn track_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 48
        && id
            .split(':')
            .all(|part| !part.is_empty() && part.bytes().all(|b| b.is_ascii_digit()))
}

impl AliceMusic {
    pub fn main_log(&mut self, line: &str) -> (bool, Option<String>) {
        #[derive(Deserialize)]
        struct Event {
            id: String,
            #[serde(default)]
            reason: String,
        }
        let Some((event, payload)) = line
            .rsplit_once("event=")
            .and_then(|(_, part)| part.split_once(", payload="))
        else {
            return (false, None);
        };
        if !matches!(event, "track_start" | "track_finished") {
            return (false, None);
        }
        let Ok(ev) = serde_json::from_str::<Event>(payload.trim()) else {
            return (false, None);
        };
        if !track_id(&ev.id) {
            return (false, None);
        }
        let now = Instant::now();
        if event == "track_finished" {
            if ev.id == self.id {
                if ev.reason == "cancel" {
                    self.pause();
                } else {
                    self.playback = None;
                    self.pos = 0.0;
                }
            }
            return (false, None);
        }
        let changed = self.id != ev.id;
        let started = changed || !self.playing();
        if changed {
            self.id = ev.id;
            self.track = Track::default();
            self.pos = 0.0;
        } else if matches!(self.playback, Some(Playback::Paused(at)) if at.elapsed() >= Duration::from_secs(7200))
        {
            self.pos = 0.0;
        }
        if started {
            self.playback = Some(Playback::Playing(now));
        }
        (started, changed.then(|| self.id.clone()))
    }

    pub fn media_log(&mut self, line: &str) -> bool {
        let Some((_, stream)) = line.split_once("[audio stream=") else {
            return false;
        };
        let id = stream
            .split(|c: char| !(c.is_ascii_digit() || c == ':'))
            .next()
            .unwrap_or("");
        if id.is_empty() || id != self.id || self.playback.is_none() {
            return false;
        }
        if let Some((_, pos)) = stream.split_once("] pause (position ") {
            if let Some(ms) = pos
                .split(|c: char| !c.is_ascii_digit())
                .next()
                .and_then(|n| n.parse::<u64>().ok())
            {
                self.pos = ms as f64 / 1000.0;
                self.playback = Some(Playback::Paused(Instant::now()));
            }
        } else if let Some((_, status)) = line.split_once("Notification: ") {
            match status.split(|c: char| !c.is_ascii_alphabetic()).next() {
                Some("Paused") => self.pause(),
                Some("Start" | "Resumed" | "Playing") if !self.playing() => {
                    self.playback = Some(Playback::Playing(Instant::now()));
                    return true;
                }
                _ => {}
            }
        }
        false
    }

    fn pause(&mut self) {
        if self.playing() {
            self.pos = self.position();
        }
        self.playback = Some(Playback::Paused(Instant::now()));
    }

    fn position(&self) -> f64 {
        let pos = if let Some(Playback::Playing(at)) = self.playback {
            self.pos + at.elapsed().as_secs_f64()
        } else {
            self.pos
        };
        let dur = self.track.duration_ms as f64 / 1000.0;
        if dur > 0.0 { pos.min(dur) } else { pos }
    }

    pub fn playing(&self) -> bool {
        matches!(self.playback, Some(Playback::Playing(_)))
    }

    pub fn paused_since(&self) -> Option<Instant> {
        match self.playback {
            Some(Playback::Paused(at)) if at.elapsed() < Duration::from_secs(1800) => Some(at),
            _ => None,
        }
    }

    pub fn active(&self) -> bool {
        self.playing() || self.paused_since().is_some()
    }

    pub fn snapshot(&self) -> Value {
        let cover = self.track.cover_uri.trim_start_matches("https://");
        let cover = if cover.is_empty() {
            String::new()
        } else {
            format!("https://{}", cover.replace("%%", "400x400"))
        };
        let title = if self.track.title.is_empty() {
            "Яндекс Музыка"
        } else {
            &self.track.title
        };
        json!({"source":"yandex", "playing":self.playing(), "paused":!self.playing(),
            "track":{"ID":self.id,"Title":title,"Author":self.track.artists.iter().map(|a| a.name.as_str()).collect::<Vec<_>>().join(", ")},
            "pos":self.position(), "dur":self.track.duration_ms as f64 / 1000.0, "cover":cover,
            "album":self.track.albums.first().map(|a| a.title.as_str()).unwrap_or(""),
            "link":format!("https://music.yandex.ru/track/{}",self.id.split(':').next().unwrap_or(""))})
    }

    pub fn description(&self) -> String {
        let action = if self.playing() {
            "Играет"
        } else {
            "На паузе"
        };
        if self.track.title.is_empty() {
            format!("{action} Яндекс Музыка.")
        } else {
            format!("{action} «{}».", self.track.title)
        }
    }

    pub fn command(&self, op: &str, args: &Value) -> Result<String, String> {
        let cmd = match op {
            "pause" => "пауза",
            "resume" => "продолжи",
            "next" | "related" => "следующий трек",
            "prev" => "предыдущий трек",
            "stop" => "стоп",
            "seek" => {
                let delta = args["delta"]
                    .as_f64()
                    .filter(|x| x.is_finite())
                    .ok_or("Неверная позиция")?;
                let relative = if args["abs"].as_bool() == Some(true) {
                    delta - self.position()
                } else {
                    delta
                };
                let seconds = relative.round() as i64;
                if seconds == 0 {
                    return Ok(String::new());
                }
                let direction = if seconds < 0 {
                    "назад"
                } else {
                    "вперёд"
                };
                return Ok(format!(
                    "перемотай {direction} на {} секунд",
                    seconds.unsigned_abs()
                ));
            }
            _ => return Err("Эта команда не поддерживается Яндекс Музыкой".into()),
        };
        Ok(cmd.into())
    }

    pub fn set_track(&mut self, id: &str, track: Track) {
        if self.id == id {
            self.track = track;
        }
    }
}

pub async fn lookup(id: &str) -> Option<Track> {
    #[derive(Deserialize)]
    struct Response {
        result: Vec<Track>,
    }
    if !track_id(id) {
        return None;
    }
    let mut response = crate::net::direct()
        .get(format!("https://api.music.yandex.net/tracks/{id}"))
        .timeout(Duration::from_secs(8))
        .send()
        .await
        .ok()?
        .error_for_status()
        .ok()?;
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.ok()? {
        if bytes.len() + chunk.len() > 1 << 20 {
            return None;
        }
        bytes.extend_from_slice(&chunk);
    }
    serde_json::from_slice::<Response>(&bytes)
        .ok()?
        .result
        .into_iter()
        .next()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn station_logs_preserve_pause_position_and_ignore_unrelated_streams() {
        let mut music = AliceMusic::default();
        assert_eq!(
            music.main_log(r#"[maind] event=track_start, payload={"id":"42"}"#),
            (true, Some("42".into()))
        );
        assert!(music.playing());
        music.media_log("[audio stream=41] pause (position 6000)");
        assert!(music.playing());
        music.media_log("[audio stream=42] pause (position 12000)");
        assert!(music.active());
        assert_eq!(music.snapshot()["pos"], 12.0);
        assert_eq!(
            music
                .command("seek", &json!({"delta":15,"abs":true}))
                .unwrap(),
            "перемотай вперёд на 3 секунд"
        );
        assert_eq!(
            music.main_log(r#"event=track_start, payload={"id":"42"}"#),
            (true, None)
        );
        assert!(music.position() >= 12.0);
        music.main_log(r#"event=track_finished, payload={"id":"42","reason":"eof"}"#);
        assert!(!music.active());
    }
}
