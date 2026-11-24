use super::{Auth, Xiaomi};
use base64::{Engine, engine::general_purpose::STANDARD as B64};
use jarvis_home_common::{bytes, now, random};
use ring::digest::{SHA1_FOR_LEGACY_USE_ONLY, SHA256, digest};
use serde_json::Value;
use zeroize::Zeroizing;

pub const REGIONS: [&str; 8] = ["de", "ru", "cn", "us", "sg", "i2", "tw", "in"];
const ACCESS: &str = "Xiaomi не принял вход";

fn signature(path: &str, fields: &[(&str, &str)], signed: &str) -> String {
    let mut input = format!("POST&{path}");
    for (key, value) in fields {
        input.push_str(&format!("&{key}={value}"));
    }
    input.push('&');
    input.push_str(signed);
    B64.encode(digest(&SHA1_FOR_LEGACY_USE_ONLY, input.as_bytes()))
}

// The existing Mi Home account protocol requires RC4 with the first 1024 bytes discarded.
fn rc4(key: &[u8; 32], data: &[u8]) -> Vec<u8> {
    let mut state = Zeroizing::new(std::array::from_fn::<_, 256, _>(|i| i as u8));
    let mut j = 0u8;
    for i in 0..256 {
        j = j.wrapping_add(state[i]).wrapping_add(key[i % key.len()]);
        state.swap(i, j as usize);
    }
    let (mut i, mut j) = (0u8, 0u8);
    let mut next = || {
        i = i.wrapping_add(1);
        j = j.wrapping_add(state[i as usize]);
        state.swap(i as usize, j as usize);
        state[state[i as usize].wrapping_add(state[j as usize]) as usize]
    };
    for _ in 0..1024 {
        next();
    }
    data.iter().map(|b| b ^ next()).collect()
}

#[cfg(test)]
#[test]
fn mi_home_signing_matches_legacy_protocol_vector() {
    let material = b"0123456789abcdefnonce-123456";
    let key: [u8; 32] = digest(&SHA256, material).as_ref().try_into().unwrap();
    let signed = B64.encode(key);
    let data = r#"{"fg": true}"#;
    assert_eq!(signed, "D8h2X9yithkRSJ04SQTRiYdB5PkJzyOWBaZ5zE2FRVc=");
    assert_eq!(
        signature("/v2/homeroom/gethome", &[("data", data)], &signed),
        "GTk+Um08ED5XQiz5J9CVt1m72KU="
    );
    assert_eq!(B64.encode(rc4(&key, data.as_bytes())), "1Tz3eUpTz3RbKUMG");
}

impl Xiaomi {
    pub async fn call(&self, region: &str, path: &str, data: &Value) -> Result<Value, String> {
        let auth = self.auth().await?;
        let response = self.call_with(&auth, region, path, data).await;
        if response.as_ref().is_err_and(|e| e == ACCESS) {
            let _gate = self.account.lock().await;
            let current = self.auth().await?;
            if current.user_id != auth.user_id {
                return Err("Аккаунт Xiaomi изменился".into());
            }
            let renewed = if current.service_token == auth.service_token
                && current.ssecurity == auth.ssecurity
            {
                self.renew(&current).await?
            } else {
                current
            };
            drop(_gate);
            return self.call_with(&renewed, region, path, data).await;
        }
        response
    }

    async fn call_with(
        &self,
        auth: &Auth,
        region: &str,
        path: &str,
        data: &Value,
    ) -> Result<Value, String> {
        if !auth.signed_in() {
            return Err("Xiaomi Home не подключён".into());
        }
        if !REGIONS.contains(&region) && !region.is_empty() {
            return Err("Неизвестный регион Xiaomi".into());
        }
        let host = if matches!(region, "" | "cn") {
            "api.io.mi.com".to_owned()
        } else {
            format!("{region}.api.io.mi.com")
        };
        let mut nonce = random::<12>()?;
        nonce[8..].copy_from_slice(&((now() / 60_000) as u32).to_be_bytes());
        let mut material = Zeroizing::new(
            B64.decode(&auth.ssecurity)
                .map_err(|_| "Неверный ключ Xiaomi")?,
        );
        material.extend(nonce);
        let key =
            Zeroizing::new(<[u8; 32]>::try_from(digest(&SHA256, &material).as_ref()).unwrap());
        let signed = Zeroizing::new(B64.encode(&*key));
        let raw = data.to_string();
        let hash = signature(path, &[("data", &raw)], &signed);
        let enc = B64.encode(rc4(&key, raw.as_bytes()));
        let enc_hash = B64.encode(rc4(&key, hash.as_bytes()));
        let sign = signature(path, &[("data", &enc), ("rc4_hash__", &enc_hash)], &signed);
        let nonce_b64 = B64.encode(nonce);
        let response=self.host.client().await?.post(format!("https://{host}/app{path}"))
            .query(&[("data",enc.as_str()),("rc4_hash__",&enc_hash),("signature",&sign),("ssecurity",&auth.ssecurity),("_nonce",&nonce_b64)])
            .header("User-Agent",&self.agent)
            .header("Accept-Encoding","identity")
            .header("Content-Type","application/x-www-form-urlencoded")
            .header("x-xiaomi-protocal-flag-cli","PROTOCAL-HTTP2")
            .header("MIOT-ENCRYPT-ALGORITHM","ENCRYPT-RC4")
            .header("Cookie",format!("userId={}; yetAnotherServiceToken={}; serviceToken={}; locale=ru_RU; timezone=GMT+00:00; channel=MI_APP_STORE",auth.user_id,auth.service_token,auth.service_token))
            .send().await.map_err(|_|"Xiaomi недоступен")?;
        if matches!(response.status().as_u16(), 401 | 403) {
            return Err(ACCESS.into());
        }
        let body = bytes(response, 8 << 20).await?;
        let encoded = std::str::from_utf8(&body)
            .map_err(|_| "Неверный ответ Xiaomi")?
            .trim();
        let raw = B64.decode(encoded).map_err(|_| "Неверный ответ Xiaomi")?;
        let envelope: Value =
            serde_json::from_slice(&rc4(&key, &raw)).map_err(|_| "Неверный ответ Xiaomi")?;
        let code = envelope["code"].as_i64().ok_or("Нет кода ответа Xiaomi")?;
        if matches!(code, 2 | 3)
            || envelope["message"]
                .as_str()
                .is_some_and(|s| s.to_ascii_lowercase().contains("auth"))
        {
            return Err(ACCESS.into());
        }
        if code != 0 {
            return Err(format!("Xiaomi ответил кодом {code}"));
        }
        Ok(envelope["result"].clone())
    }
}
