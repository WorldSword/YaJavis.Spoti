use super::*;
use model::{Device, actions_for, hex_rgb};
use std::collections::BTreeSet;

fn fully_supported(d: &Device, a: &Args) -> bool {
    (a.on.is_none() || d.cap("on_off", "").is_some())
        && (a.color.is_empty()
            || d.cap("color_setting", "").is_some_and(|c| {
                ["rgb", "hsv"].contains(&c.parameters["color_model"].as_str().unwrap_or(""))
            }))
        && (a.brightness <= 0 || d.cap("range", "brightness").is_some())
        && (a.temperature_k <= 0
            || d.cap("color_setting", "")
                .is_some_and(|c| c.parameters["temperature_k"].is_object()))
        && a.ranges.keys().all(|i| d.cap("range", i).is_some())
        && a.toggles.keys().all(|i| d.cap("toggle", i).is_some())
        && a.modes.iter().all(|(i, v)| {
            d.cap("mode", i).is_some_and(|c| {
                c.parameters["modes"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .any(|m| m["value"] == *v)
            })
        })
}
fn confirmed(sent: &Value, response: &Value) -> bool {
    let found: Vec<_> = response["devices"]
        .as_array()
        .into_iter()
        .flatten()
        .filter(|d| d["id"] == sent["id"])
        .collect();
    if found.len() != 1 {
        return false;
    }
    let result = found[0];
    sent["actions"].as_array().is_some_and(|actions| {
        !actions.is_empty()
            && actions.iter().all(|a| {
                let found: Vec<_> = result["capabilities"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter(|c| {
                        c["type"] == a["type"] && c["state"]["instance"] == a["state"]["instance"]
                    })
                    .collect();
                found.len() == 1 && found[0]["state"]["action_result"]["status"] == "DONE"
            })
    })
}
impl Call<'_> {
    pub(crate) async fn apply(&self, a: &Args) -> String {
        if !a.color.is_empty() && hex_rgb(&a.color).is_none() {
            return "Неверный цвет: нужен #rrggbb.".into();
        }
        let c = match self.catalog(false).await {
            Ok(c) => c,
            Err(_) => return "Не удалось связаться с Яндекс Умным домом.".into(),
        };
        if !a.scenario_id.is_empty() {
            if !c
                .scenarios
                .iter()
                .any(|s| s.id == a.scenario_id && s.is_active != Some(false))
            {
                return "Не нашёл активный сценарий.".into();
            }
            let result = self
                .iot(
                    reqwest::Method::POST,
                    &["scenarios", &a.scenario_id, "actions"],
                    None,
                )
                .await;
            self.state.invalidate();
            return if result.is_ok() {
                "Готово."
            } else {
                "Сценарий не запустился."
            }
            .into();
        }
        if a.device_ids.is_empty() {
            return if self.alice {"Не указано, какие устройства. Если нужного устройства нет в списке — передай команду Алисе инструментом alice_command."}else{"Не указано, какие устройства."}.into();
        }
        let ids: BTreeSet<_> = a.device_ids.iter().collect();
        let (mut failed, mut unsupported, mut attempted, mut partial, mut unknown) =
            (0, 0, 0, false, 0);
        let mut devices = vec![];
        for id in ids {
            if let Some(group) = c.groups.iter().find(|g| g.id == *id) {
                let only_on = a.on.is_some()
                    && a.color.is_empty()
                    && a.temperature_k <= 0
                    && a.brightness <= 0
                    && a.ranges.is_empty()
                    && a.modes.is_empty()
                    && a.toggles.is_empty();
                if !only_on || group.devices.is_empty() {
                    unsupported += 1;
                    continue;
                }
                attempted += 1;
                let response=self.iot(reqwest::Method::POST,&["groups",id,"actions"],Some(json!({"actions":[{"type":"devices.capabilities.on_off","state":{"instance":"on","value":a.on}}]}))).await;
                let confirmed_group=response.is_ok_and(|r|r["devices"].as_array().is_some_and(|ds|ds.len()==group.devices.len() && group.devices.iter().all(|id|confirmed(&json!({"id":id,"actions":[{"type":"devices.capabilities.on_off","state":{"instance":"on"}}]}),&r))));
                if !confirmed_group {
                    failed += 1;
                }
                continue;
            }
            let Some(d) = c.devices.iter().find(|d| d.id == *id) else {
                unknown += 1;
                continue;
            };
            let actions = actions_for(d, a);
            if actions.is_empty() {
                unsupported += 1;
                continue;
            }
            partial |= !fully_supported(d, a);
            attempted += 1;
            devices.push(json!({"id":id,"actions":actions}));
        }
        if !devices.is_empty() {
            match self
                .iot(
                    reqwest::Method::POST,
                    &["devices", "actions"],
                    Some(json!({"devices":devices})),
                )
                .await
            {
                Ok(response) => {
                    for sent in &devices {
                        if !confirmed(sent, &response) {
                            failed += 1;
                        }
                    }
                }
                Err(_) => {
                    self.state.invalidate();
                    return "Не удалось связаться с Яндекс Умным домом. Выполнение не подтверждено.".into();
                }
            }
        }
        if attempted > 0 {
            self.state.invalidate();
        }
        if attempted == 0 {
            return if unsupported > 0 {
                "Эти устройства так не умеют."
            } else {
                "Не нашёл такие устройства."
            }
            .into();
        }
        if failed == 0 && unsupported == 0 && unknown == 0 && !partial {
            "Готово.".into()
        } else if failed < attempted && (unsupported > 0 || unknown > 0 || partial) {
            "Часть действий выполнена; не все устройства найдены или поддерживают выбранные параметры.".into()
        } else if failed < attempted {
            format!("Готово, но {failed} из {attempted} не ответили.")
        } else {
            "Яндекс не подтвердил выполнение всех запрошенных действий.".into()
        }
    }
}
