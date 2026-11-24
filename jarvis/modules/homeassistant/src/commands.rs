use super::*;
pub(crate) fn room_mention(text: &str, name: &str) -> bool {
    let n = norm(name);
    let text = norm(text);
    let words: Vec<_> = text.split_whitespace().collect();
    let stem = |s: &str| {
        s.trim_end_matches(|c: char| "аяуюеёиыоьй".contains(c))
            .to_string()
    };
    !n.is_empty()
        && n.split_whitespace().all(|part| {
            words
                .iter()
                .any(|w| *w == part || (part.chars().count() >= 4 && stem(w) == stem(part)))
        })
}
pub(crate) fn binding_name(r: &Room, name: &str) -> bool {
    norm(&r.name) == norm(name) || r.aliases.iter().any(|a| norm(a) == norm(name))
}
pub(crate) fn binding_mention(text: &str, r: &Room) -> bool {
    room_mention(text, &r.name) || r.aliases.iter().any(|a| room_mention(text, a))
}
impl Client {
    pub(crate) fn intent(&self, text: &str) -> bool {
        if !self.cfg.ready() {
            return false;
        }
        let n = bare(text);
        if ![
            "включи ",
            "запусти ",
            "убери ",
            "пропылесось ",
            "начни ",
            "останови ",
            "выключи ",
            "выруби ",
            "отключи ",
            "прекрати ",
            "продолжи ",
            "поставь ",
            "верни ",
            "отправь ",
            "что с ",
            "где ",
            "как там ",
        ]
        .iter()
        .any(|v| n.starts_with(v))
        {
            return false;
        }
        ["пылесос", "пылик", "уборк"].iter().any(|w| n.contains(w))
            || ((n.starts_with("убери ") || n.starts_with("пропылесось "))
                && (self.cfg.rooms.iter().any(|r| binding_mention(&n, r))
                    || [
                        "кухн",
                        "спальн",
                        "комнат",
                        "гостин",
                        "коридор",
                        "прихож",
                        "ванн",
                        "туалет",
                        "кабинет",
                        " зал",
                    ]
                    .iter()
                    .any(|w| n.contains(w))))
    }
    pub(crate) async fn vacuum_command(&self, text: &str) -> String {
        let n = bare(text);
        if n.split_whitespace().count() > 18 || text.chars().any(char::is_control) {
            return "Скажи коротко, что сделать с пылесосом.".into();
        }
        let mut action = if n.contains("на баз") || n.contains("на заряд") {
            "return_to_base"
        } else if n.contains("пауз") {
            "pause"
        } else if ["останови ", "выключи ", "выруби ", "отключи ", "прекрати "]
            .iter()
            .any(|v| n.starts_with(v))
        {
            "stop"
        } else if ["где ", "что с ", "как там "]
            .iter()
            .any(|v| n.starts_with(v))
        {
            "status"
        } else {
            "start"
        };
        let entities = match self.catalog(false).await {
            Ok(v) => v,
            Err(e) => return e,
        };
        let vacuums: Vec<_> = entities.iter().filter(|e| e.domain() == "vacuum").collect();
        let matching: Vec<_> = vacuums
            .iter()
            .filter(|e| n.contains(&norm(e.name())))
            .collect();
        let entity = if vacuums.len() == 1 {
            vacuums[0].entity_id.as_str()
        } else if matching.len() == 1 {
            matching[0].entity_id.as_str()
        } else {
            ""
        };
        let mut room = "";
        if action == "start" {
            for r in &self.cfg.rooms {
                if binding_mention(&n, r)
                    && (entity.is_empty() || r.vacuum.is_empty() || r.vacuum == entity)
                {
                    if !room.is_empty() && room != r.name {
                        return "Пока назови одну комнату для уборки.".into();
                    }
                    room = &r.name;
                }
            }
            if !room.is_empty() {
                action = "clean_room";
            } else if n.starts_with("убери ")
                || n.starts_with("пропылесось ")
                || [" в ", " на кухн", "комнат", "спальн", "коридор", "гостин"]
                    .iter()
                    .any(|w| format!(" {n} ").contains(w))
            {
                return "Для этой комнаты ещё не настроена уборка в подключении Home Assistant. Всю квартиру вместо неё не запускаю.".into();
            }
        }
        if entity.is_empty() && action != "clean_room" {
            return if vacuums.is_empty() {
                "В Home Assistant пока нет пылесоса."
            } else {
                "В Home Assistant несколько пылесосов. Назови нужный."
            }
            .into();
        }
        self.control(json!({"entity_id":entity,"action":action,"room":room}))
            .await
    }
}
