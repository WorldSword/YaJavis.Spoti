use super::*;
use commands::binding_name;
impl Client {
    pub(crate) async fn control(&self, args: Value) -> String {
        match self.try_control(args).await {
            Ok(s) => s,
            Err(e) => e,
        }
    }
    async fn try_control(&self, args: Value) -> Result<String, String> {
        let a = args.as_object().ok_or("Не понял команду Home Assistant.")?;
        let get = |key: &str| a.get(key).and_then(Value::as_str).unwrap_or("");
        let (mut entity, action, room) = (get("entity_id").to_string(), get("action"), get("room"));
        let entities = self.catalog(true).await?;
        let find = |id: &str| entities.iter().find(|e| e.entity_id == id);
        let mut domain = entity.split('.').next().unwrap_or("").to_string();
        let mut data = json!({"entity_id":entity});
        let mut result_entity = String::new();
        let mut result_vacuum = String::new();
        let service;
        if action == "clean_room" {
            if room.trim().is_empty() {
                return Err("Какую комнату убрать?".into());
            }
            let bindings: Vec<_> = self
                .cfg
                .rooms
                .iter()
                .filter(|r| binding_name(r, room) && (entity.is_empty() || r.vacuum == entity))
                .collect();
            if bindings.is_empty() {
                return Err(format!(
                    "Уборка комнаты «{room}» ещё не настроена для этого пылесоса. Всю квартиру вместо неё не запускаю."
                ));
            }
            if bindings.len() != 1 {
                return Err("Для этой комнаты несколько пылесосов. Уточни устройство.".into());
            }
            let r = bindings[0];
            result_entity = r.result_entity.clone();
            result_vacuum = r.vacuum.clone();
            let mut ids = r.room_ids.clone();
            if !r.info_entity.is_empty() {
                let map = find(&r.info_entity)
                    .and_then(rooms::parse_rooms)
                    .ok_or("Не получилось прочитать актуальную карту комнат пылесоса.")?;
                let r=map.rooms.iter().find(|v|v.name==r.info_room).ok_or("Комната на карте пылесоса изменилась. Обнови список комнат в подключении Home Assistant.")?;
                ids = match &r.id {
                    Value::String(s) => s.clone(),
                    Value::Number(n) => n.to_string(),
                    _ => return Err("Неверный номер комнаты на карте пылесоса.".into()),
                };
            }
            entity = r.entity.clone();
            domain = entity.split('.').next().unwrap_or("").into();
            data = json!({"entity_id":entity});
            service = match domain.as_str() {
                "script" => "turn_on",
                "notify" | "text" => {
                    if ids.is_empty() {
                        return Err("У комнаты не указаны номера зон на карте пылесоса.".into());
                    }
                    // Xiaomi action debug разбирает YAML: список зон должен оставаться строкой.
                    let value = format!("'{}'", ids.replace('\'', "''"));
                    if domain == "notify" {
                        data["message"] = value.into();
                        "send_message"
                    } else {
                        data["value"] = value.into();
                        "set_value"
                    }
                }
                _ => return Err(
                    "Для комнаты нужен сценарий уборки или действие пылесоса из Home Assistant."
                        .into(),
                ),
            };
        } else {
            if action == "status" {
                let e = find(&entity).ok_or("Устройство Home Assistant не найдено.")?;
                return Ok(format!("{}: {}.", e.name(), state_text(&e.state)));
            }
            service = match (action, domain.as_str()) {
                ("turn_on" | "turn_off", "light" | "switch" | "fan" | "climate") => action,
                ("turn_on", "script" | "scene") => "turn_on",
                ("start" | "pause" | "stop" | "return_to_base", "vacuum") => action,
                _ => return Err("Такое действие для этого устройства не поддерживается.".into()),
            };
        }
        let e = find(&entity)
            .ok_or("Устройство или сценарий Home Assistant не найден. Обнови подключение.")?;
        if !valid_entity(&entity) {
            return Err("Неверный entity_id".into());
        }
        if e.state == "unavailable" {
            return Err(format!("Устройство «{}» сейчас недоступно.", e.name()));
        }
        let requested = jiff::Timestamp::now();
        if domain == "vacuum" && action == "stop" {
            let _ = self.service("vacuum", "pause", data.clone()).await;
            // Робот останавливается и при недоступном реестре базы.
            self.service("vacuum", "stop", data.clone()).await?;
            let _ =
                tokio::time::timeout(Duration::from_secs(3), self.stop_base(&entity, &entities))
                    .await;
            let _ = self.service("vacuum", "pause", data.clone()).await;
        }
        self.service(&domain, service, data)
            .await
            .map_err(|e| format!("Команда не выполнена: {e}"))?;
        self.cache.lock().unwrap().fetched = None;
        if action == "clean_room" {
            if !result_entity.is_empty() {
                let wait = async {
                    loop {
                        if !result_vacuum.is_empty() {
                            if let Ok(robot) = self.state(&result_vacuum).await {
                                // Для факта начала уборки требуется свежий last_updated.
                                if robot.state == "cleaning"
                                    && robot
                                        .last_updated
                                        .parse::<jiff::Timestamp>()
                                        .is_ok_and(|t| t > requested)
                                {
                                    return Some(format!(
                                        "Пылесос начал уборку по команде для комнаты «{room}»."
                                    ));
                                }
                            }
                        }
                        if let Ok(result) = self.state(&result_entity).await {
                            if result.newer_than(requested) {
                                let ack: Value =
                                    serde_json::from_str(&result.state).unwrap_or_default();
                                if let Some(code) = ack["code"].as_i64() {
                                    return Some(if code == 0 {
                                        format!(
                                            "Пылесос подтвердил команду уборки комнаты «{room}»."
                                        )
                                    } else {
                                        format!(
                                            "Пылесос отклонил уборку комнаты «{room}», код {code}."
                                        )
                                    });
                                }
                            }
                        }
                        tokio::time::sleep(Duration::from_millis(400)).await;
                    }
                };
                if let Ok(Some(result)) = tokio::time::timeout(Duration::from_secs(6), wait).await {
                    return Ok(result);
                }
            }
            return Ok(format!(
                "Команда уборки комнаты «{room}» передана. Жду подтверждения от пылесоса."
            ));
        }
        if ["vacuum", "light", "switch"].contains(&domain.as_str()) {
            let wait = async {
                loop {
                    if let Ok(current) = self.state(&entity).await {
                        let matched = match action {
                            "start" => current.state == "cleaning",
                            "pause" => current.state == "paused",
                            "stop" => {
                                ["idle", "docked", "paused"].contains(&current.state.as_str())
                            }
                            "return_to_base" => {
                                ["returning", "docked"].contains(&current.state.as_str())
                            }
                            "turn_on" => current.state == "on",
                            "turn_off" => current.state == "off",
                            _ => false,
                        };
                        if matched {
                            return format!("{}: {}.", current.name(), state_text(&current.state));
                        }
                        if action == "return_to_base" && current.state == "idle" {
                            let prefix = format!(
                                "sensor.{}_charging_state_",
                                entity.trim_start_matches("vacuum.")
                            );
                            if self
                                .catalog(true)
                                .await
                                .unwrap_or_default()
                                .iter()
                                .any(|e| {
                                    e.entity_id.starts_with(&prefix)
                                        && e.state.eq_ignore_ascii_case("charging")
                                })
                            {
                                return format!("{}: на базе, заряжается.", current.name());
                            }
                        }
                    }
                    tokio::time::sleep(Duration::from_millis(400)).await;
                }
            };
            if let Ok(result) = tokio::time::timeout(Duration::from_secs(3), wait).await {
                return Ok(result);
            }
        }
        Ok(format!(
            "Команда для «{}» передана в Home Assistant. Подтверждения от устройства пока нет.",
            e.name()
        ))
    }
}
