use super::{Auth, Xiaomi, cloud::REGIONS};
use jarvis_home_common::{save, text};
use serde::{
    Deserialize, Serialize,
    de::{DeserializeSeed, IgnoredAny, MapAccess, SeqAccess, Visitor},
};
use serde_json::{Value, json};
use std::collections::{HashMap, HashSet};
use std::io::{self, Read};

#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(default)]
struct Property {
    name: String,
    siid: u32,
    piid: u32,
    format: String,
    access: String,
    range: Vec<Value>,
    values: Vec<String>,
    unit: String,
}
#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(default)]
struct Action {
    name: String,
    siid: u32,
    aiid: u32,
    #[serde(default)]
    r#in: Vec<u32>,
}
#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct Device {
    did: String,
    name: String,
    room: String,
    region: String,
    model: String,
    #[serde(rename = "type")]
    kind: String,
    online: bool,
    props: Vec<Property>,
    actions: Vec<Action>,
}
impl Device {
    pub fn view(&self) -> Value {
        json!({"name":self.name,"room":self.room,"type":self.kind,"model":self.model,"region":self.region,"online":self.online})
    }
}
fn array(value: &Value) -> impl Iterator<Item = &Value> {
    value.as_array().into_iter().flatten()
}
fn urn(value: &Value) -> &str {
    let raw = value.as_str().unwrap_or("");
    raw.split(':').nth(3).unwrap_or(raw)
}
fn number(value: &Value) -> Option<u64> {
    value.as_u64().or_else(|| value.as_str()?.parse().ok())
}

impl Xiaomi {
    async fn regional_devices(&self, region: &str) -> Result<Vec<Device>, String> {
        let homes=self.call(region,"/v2/homeroom/gethome",&json!({"fg":true,"fetch_share":true,"fetch_share_dev":true,"limit":300,"app_ver":7})).await?;
        let mut devices = vec![];
        let mut seen = HashSet::new();
        for home in array(&homes["homelist"]).take(300) {
            let id = number(&home["id"]).ok_or("Неверный ID дома Xiaomi")?;
            let uid = number(&home["uid"]).unwrap_or_default();
            let rooms: HashMap<_, _> = array(&home["roomlist"])
                .flat_map(|room| {
                    array(&room["dids"]).map(move |did| (text(did), text(&room["name"])))
                })
                .collect();
            let list=self.call(region,"/v2/home/home_device_list",&json!({"home_owner":uid,"home_id":id,"limit":200,"get_split_device":true,"support_smart_home":true})).await?;
            for item in array(&list["device_info"]).take(200) {
                let did = text(&item["did"]);
                if did.is_empty() || !seen.insert(did.clone()) {
                    continue;
                }
                devices.push(Device {
                    room: rooms.get(&did).cloned().unwrap_or_default(),
                    did,
                    name: text(&item["name"]),
                    region: region.into(),
                    model: text(&item["model"]),
                    kind: text(&item["spec_type"]),
                    online: item["isOnline"] == true,
                    ..Default::default()
                });
            }
        }
        Ok(devices)
    }
    pub async fn refresh(self: &std::sync::Arc<Self>) -> Result<usize, String> {
        let _catalog = self.catalog.lock().await;
        let initial = self.auth().await?;
        if !initial.signed_in() {
            return Err("Xiaomi Home не подключён".into());
        }
        let mut all = vec![];
        let mut retained = vec![];
        let mut success = 0;
        let mut last_error = String::new();
        let cached = self.devices();
        for regions in REGIONS.chunks(4) {
            let mut queue = tokio::task::JoinSet::new();
            for &region in regions {
                let this = self.clone();
                queue.spawn(async move { (region, this.regional_devices(region).await) });
            }
            let mut batch = vec![];
            while let Some(result) = queue.join_next().await {
                batch.push(result.map_err(|_| "Не обновить список Xiaomi")?);
            }
            for &region in regions {
                let (_, result) =
                    batch.swap_remove(batch.iter().position(|(id, _)| *id == region).unwrap());
                match result {
                    Ok(mut devices) => {
                        success += 1;
                        all.append(&mut devices);
                    }
                    Err(error) => {
                        last_error = error;
                        retained.extend(cached.iter().filter(|d| d.region == region).cloned());
                    }
                }
            }
        }
        if success == 0 || all.is_empty() && !last_error.is_empty() {
            return Err(last_error);
        }
        self.describe(&mut all).await;
        for device in &mut all {
            if device.props.is_empty()
                && device.actions.is_empty()
                && let Some(old) = cached
                    .iter()
                    .find(|o| o.did == device.did && o.model == device.model)
            {
                device.kind = old.kind.clone();
                device.props = old.props.clone();
                device.actions = old.actions.clone();
            }
        }
        all.extend(retained);
        let mut seen = HashSet::new();
        all.retain(|d| seen.insert(d.did.clone()));
        all.sort_by(|a, b| (&a.room, &a.name).cmp(&(&b.room, &b.name)));
        let _account = self.account.lock().await;
        let current = self.auth().await?;
        if !current.signed_in() || current.user_id != initial.user_id {
            return Err("Аккаунт Xiaomi изменился".into());
        }
        let mut updated = current.clone();
        updated.region = REGIONS
            .iter()
            .copied()
            .filter(|r| all.iter().any(|d| d.region == *r))
            .collect::<Vec<_>>()
            .join(", ");
        if current.region != updated.region {
            self.update(&current, &updated, false).await?;
        }
        save(
            &self.cache(),
            &serde_json::to_vec(&all).map_err(|_| "Не сохранить каталог Xiaomi")?,
        )?;
        let len = all.len();
        *self.devices.lock().unwrap() = Some(all);
        Ok(len)
    }

    async fn describe(&self, devices: &mut [Device]) {
        let Ok(client) = self.host.client().await else {
            return;
        };
        let missing: HashSet<_> = devices
            .iter()
            .filter(|d| d.kind.is_empty())
            .map(|d| d.model.clone())
            .collect();
        let types = if missing.is_empty() {
            HashMap::new()
        } else {
            model_types(&client, missing).await.unwrap_or_default()
        };
        let mut specs = HashMap::new();
        for d in devices {
            let kind = std::mem::take(&mut d.kind);
            let id = if kind.is_empty() {
                types.get(&d.model).map(String::as_str).unwrap_or("")
            } else {
                &kind
            };
            if !id.starts_with("urn:miot-spec-v2:device:") {
                continue;
            }
            if !specs.contains_key(id) {
                let spec = match client
                    .get("https://miot-spec.org/miot-spec-v2/instance")
                    .query(&[("type", id)])
                    .send()
                    .await
                {
                    Ok(response) => jarvis_home_common::json(response).await.unwrap_or_default(),
                    Err(_) => Value::Null,
                };
                specs.insert(id.to_owned(), spec);
            }
            let spec = &specs[id];
            d.kind = urn(spec.get("type").unwrap_or(&Value::Null)).into();
            for service in array(&spec["services"]) {
                let name = urn(&service["type"]);
                let Some(siid) = iid(&service["iid"]) else {
                    continue;
                };
                if name.is_empty() || name == "device-information" {
                    continue;
                }
                for property in array(&service["properties"]) {
                    let access: String = array(&property["access"])
                        .filter_map(|p| match p.as_str() {
                            Some("read") => Some('r'),
                            Some("write") => Some('w'),
                            _ => None,
                        })
                        .collect();
                    let Some(piid) = iid(&property["iid"]) else {
                        continue;
                    };
                    if access.is_empty() {
                        continue;
                    }
                    d.props.push(Property {
                        name: format!("{name}.{}", urn(&property["type"])),
                        siid,
                        piid,
                        access,
                        format: text(&property["format"]),
                        unit: text(&property["unit"]),
                        range: array(&property["value-range"]).cloned().collect(),
                        values: array(&property["value-list"])
                            .map(|v| format!("{}={}", text(&v["value"]), text(&v["description"])))
                            .collect(),
                    });
                }
                for action in array(&service["actions"]) {
                    let Some(aiid) = iid(&action["iid"]) else {
                        continue;
                    };
                    d.actions.push(Action {
                        name: format!("{name}.{}", urn(&action["type"])),
                        siid,
                        aiid,
                        r#in: array(&action["in"]).filter_map(iid).collect(),
                    });
                }
            }
        }
    }

    pub(super) async fn control(
        &self,
        auth: &Auth,
        devices: &[Device],
        args: &Value,
    ) -> Result<String, String> {
        let value: Value = match args.as_str() {
            Some(s) => serde_json::from_str(s).map_err(|_| "Не понял команду для Xiaomi")?,
            None => args.clone(),
        };
        let id = value["did"]
            .as_str()
            .filter(|s| !s.is_empty())
            .ok_or("Нужен ID устройства")?;
        let device = devices
            .iter()
            .find(|d| d.did == id)
            .or_else(|| {
                let mut matches = devices
                    .iter()
                    .filter(|d| d.name.to_lowercase() == id.to_lowercase());
                let found = matches.next()?;
                if matches.next().is_some() {
                    None
                } else {
                    Some(found)
                }
            })
            .ok_or("Укажите устройство из списка Xiaomi")?;
        let region = if device.region.is_empty() {
            auth.region.split(',').next().unwrap_or("").trim()
        } else {
            &device.region
        };
        let name = value["name"].as_str().unwrap_or("");
        match value["action"].as_str() {
            Some("get") => {
                let props: Vec<_> = device
                    .props
                    .iter()
                    .filter(|p| p.access.contains('r') && (name.is_empty() || p.name == name))
                    .take(30)
                    .collect();
                if props.is_empty() {
                    return Err("У устройства нет такого свойства для чтения".into());
                }
                let params: Vec<_> = props
                    .iter()
                    .map(|p| json!({"did":device.did,"siid":p.siid,"piid":p.piid}))
                    .collect();
                let result = self
                    .call(region, "/miotspec/prop/get", &json!({"params":params}))
                    .await?;
                let output: Vec<_> = array(&result)
                    .filter(|r| r["code"] == 0)
                    .filter_map(|r| {
                        let prop = props
                            .iter()
                            .find(|p| r["siid"] == p.siid && r["piid"] == p.piid)?;
                        Some(format!("{} = {}", prop.name, r["value"]))
                    })
                    .collect();
                if output.is_empty() {
                    return Err("Устройство не ответило".into());
                }
                Ok(format!("«{}»: {}", device.name, output.join("; ")))
            }
            Some("set") => {
                let prop = device
                    .props
                    .iter()
                    .find(|p| p.name == name && p.access.contains('w'))
                    .ok_or("Нет такого изменяемого свойства")?;
                let new_value = value
                    .get("value")
                    .filter(|v| !v.is_null())
                    .ok_or("Передайте значение свойства")?;
                let result=self.call(region,"/miotspec/prop/set",&json!({"params":[{"did":device.did,"siid":prop.siid,"piid":prop.piid,"value":new_value}]})).await?;
                confirm(&result[0])?;
                Ok(format!(
                    "Готово: «{}» {} = {new_value}",
                    device.name, prop.name
                ))
            }
            Some("do") => {
                let action = device
                    .actions
                    .iter()
                    .find(|a| a.name == name)
                    .ok_or("Нет такого действия")?;
                let empty = vec![];
                let input = value["params"].as_array().unwrap_or(&empty);
                if (!value["params"].is_null() && !value["params"].is_array()) || input.len() > 32 {
                    return Err("Неверные параметры действия".into());
                }
                let result=self.call(region,"/miotspec/action",&json!({"params":{"did":device.did,"siid":action.siid,"aiid":action.aiid,"in":input}})).await?;
                confirm(&result)?;
                Ok(format!("Готово: «{}» {}", device.name, action.name))
            }
            _ => Err("Неизвестная команда Xiaomi".into()),
        }
    }
}
fn iid(value: &Value) -> Option<u32> {
    number(value)
        .and_then(|v| u32::try_from(v).ok())
        .filter(|n| *n > 0)
}
fn confirm(value: &Value) -> Result<(), String> {
    match value["code"].as_i64() {
        Some(0 | 1) => Ok(()),
        Some(n) => Err(format!("Устройство отказало (код {n})")),
        None => Err("Нет подтверждения устройства".into()),
    }
}
pub fn context(devices: &[Device]) -> String {
    if devices.is_empty() {
        return String::new();
    }
    let mut out="\n\nУстройства Xiaomi Home. Используй инструмент xiaomi_home, did и точные имена свойств и действий.".to_owned();
    for d in devices {
        out.push_str(&format!(
            "\n- did {} «{}» ({}, {}, {})",
            d.did,
            d.name,
            d.room,
            d.kind,
            if d.online {
                "в сети"
            } else {
                "не в сети"
            }
        ));
        for p in &d.props {
            out.push_str(&format!(
                "; {} [{} {} {}]",
                p.name, p.format, p.access, p.unit
            ));
            if p.range.len() >= 2 {
                out.push_str(&format!(" {}..{}", p.range[0], p.range[1]));
            }
            if p.values.len() <= 12 && !p.values.is_empty() {
                out.push_str(&format!(" ({})", p.values.join(", ")));
            }
        }
        for a in &d.actions {
            out.push_str(&format!("; действие {}", a.name));
        }
    }
    out
}

// The MIoT index is parsed one entry at a time; only requested models are retained.
async fn model_types(
    client: &reqwest::Client,
    wanted: HashSet<String>,
) -> Result<HashMap<String, String>, String> {
    let mut response = client
        .get("https://miot-spec.org/miot-spec-v2/instances?status=released")
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|_| "Каталог MIoT недоступен")?;
    let (tx, rx) = tokio::sync::mpsc::channel(2);
    let parse = tokio::task::spawn_blocking(move || {
        let mut deserializer = serde_json::Deserializer::from_reader(io::BufReader::new(Chunks {
            rx,
            current: io::Cursor::new(vec![]),
        }));
        Index(&wanted)
            .deserialize(&mut deserializer)
            .map_err(|_| "Неверный каталог MIoT".to_owned())
    });
    let mut total = 0;
    while let Some(bytes) = response.chunk().await.map_err(|_| "Обрыв каталога MIoT")?
    {
        total += bytes.len();
        if total > 32 << 20 {
            return Err("Слишком большой каталог MIoT".into());
        }
        if tx.send(bytes.to_vec()).await.is_err() {
            break;
        }
    }
    drop(tx);
    parse.await.map_err(|_| "Не обработать каталог MIoT")?
}
struct Chunks {
    rx: tokio::sync::mpsc::Receiver<Vec<u8>>,
    current: io::Cursor<Vec<u8>>,
}
impl Read for Chunks {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if buf.is_empty() {
            return Ok(0);
        }
        loop {
            let n = self.current.read(buf)?;
            if n > 0 {
                return Ok(n);
            }
            let Some(chunk) = self.rx.blocking_recv() else {
                return Ok(0);
            };
            self.current = io::Cursor::new(chunk);
        }
    }
}
struct Index<'a>(&'a HashSet<String>);
struct Entries<'a>(&'a HashSet<String>);
#[derive(Deserialize)]
struct Entry {
    model: String,
    #[serde(rename = "type")]
    kind: String,
    #[serde(default)]
    version: u32,
}
impl<'de> DeserializeSeed<'de> for Index<'_> {
    type Value = HashMap<String, String>;
    fn deserialize<D: serde::Deserializer<'de>>(self, d: D) -> Result<Self::Value, D::Error> {
        d.deserialize_map(self)
    }
}
impl<'de> Visitor<'de> for Index<'_> {
    type Value = HashMap<String, String>;
    fn expecting(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("MIoT catalog")
    }
    fn visit_map<M: MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
        let mut output = HashMap::new();
        while let Some(key) = map.next_key::<String>()? {
            if key == "instances" {
                output = map.next_value_seed(Entries(self.0))?;
            } else {
                map.next_value::<IgnoredAny>()?;
            }
        }
        Ok(output)
    }
}
impl<'de> DeserializeSeed<'de> for Entries<'_> {
    type Value = HashMap<String, String>;
    fn deserialize<D: serde::Deserializer<'de>>(self, d: D) -> Result<Self::Value, D::Error> {
        d.deserialize_seq(self)
    }
}
impl<'de> Visitor<'de> for Entries<'_> {
    type Value = HashMap<String, String>;
    fn expecting(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("MIoT models")
    }
    fn visit_seq<S: SeqAccess<'de>>(self, mut seq: S) -> Result<Self::Value, S::Error> {
        let mut best = HashMap::<String, (u32, String)>::new();
        while let Some(entry) = seq.next_element::<Entry>()? {
            if self.0.contains(&entry.model)
                && best
                    .get(&entry.model)
                    .is_none_or(|(v, _)| *v <= entry.version)
            {
                best.insert(entry.model, (entry.version, entry.kind));
            }
        }
        Ok(best
            .into_iter()
            .map(|(model, (_, kind))| (model, kind))
            .collect())
    }
}
