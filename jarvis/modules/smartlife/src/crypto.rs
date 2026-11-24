use aes::{
    Aes128,
    cipher::{BlockDecrypt, BlockEncrypt, KeyInit, generic_array::GenericArray},
};
use base64::{Engine, engine::general_purpose::STANDARD as B64};
use md5::{Digest, Md5};
use ring::{aead, hmac};
pub fn md5(bytes: &[u8]) -> Vec<u8> {
    Md5::digest(bytes).to_vec()
}
pub fn mac(key: &[u8], bytes: &[u8]) -> Vec<u8> {
    hmac::sign(&hmac::Key::new(hmac::HMAC_SHA256, key), bytes)
        .as_ref()
        .to_vec()
}
pub fn verify(key: &[u8], data: &[u8], tag: &[u8]) -> Result<(), String> {
    hmac::verify(&hmac::Key::new(hmac::HMAC_SHA256, key), data, tag)
        .map_err(|_| "Неверная подпись Tuya".into())
}
pub fn ecb(key: &[u8], data: &[u8], encrypt: bool, padding: bool) -> Result<Vec<u8>, String> {
    let cipher = Aes128::new_from_slice(key).map_err(|_| "Неверный ключ Tuya")?;
    let mut out = data.to_vec();
    if encrypt && padding {
        let pad = 16 - out.len() % 16;
        out.resize(out.len() + pad, pad as u8);
    }
    if out.is_empty() || out.len() % 16 != 0 {
        return Err("Неверный блок Tuya".into());
    }
    for block in out.chunks_exact_mut(16) {
        let block = GenericArray::from_mut_slice(block);
        if encrypt {
            cipher.encrypt_block(block);
        } else {
            cipher.decrypt_block(block);
        }
    }
    if !encrypt && padding {
        let pad = *out.last().unwrap() as usize;
        if pad == 0
            || pad > 16
            || pad > out.len()
            || !out[out.len() - pad..].iter().all(|b| *b as usize == pad)
        {
            return Err("Неверное дополнение Tuya".into());
        }
        out.truncate(out.len() - pad);
    }
    Ok(out)
}
pub fn seal(key: &[u8], nonce: [u8; 12], data: &[u8], aad: &[u8]) -> Result<Vec<u8>, String> {
    let key = aead::LessSafeKey::new(
        aead::UnboundKey::new(&aead::AES_128_GCM, key).map_err(|_| "Неверный ключ Tuya")?,
    );
    let mut out = data.to_vec();
    key.seal_in_place_append_tag(
        aead::Nonce::assume_unique_for_key(nonce),
        aead::Aad::from(aad),
        &mut out,
    )
    .map_err(|_| "Не зашифровать Tuya")?;
    Ok(out)
}
pub fn open(key: &[u8], nonce: [u8; 12], data: &[u8], aad: &[u8]) -> Result<Vec<u8>, String> {
    let key = aead::LessSafeKey::new(
        aead::UnboundKey::new(&aead::AES_128_GCM, key).map_err(|_| "Неверный ключ Tuya")?,
    );
    let mut out = data.to_vec();
    let plain = key
        .open_in_place(
            aead::Nonce::assume_unique_for_key(nonce),
            aead::Aad::from(aad),
            &mut out,
        )
        .map_err(|_| "Неверная подпись Tuya GCM")?;
    Ok(plain.to_vec())
}
pub fn cloud_secret(rid: &str, refresh: &str) -> (String, Vec<u8>) {
    let hash = hex::encode(md5(format!("{rid}{refresh}").as_bytes()));
    let secret = hex::encode(mac(rid.as_bytes(), hash.as_bytes()))[..16]
        .as_bytes()
        .to_vec();
    (hash, secret)
}
pub fn encrypt(data: &serde_json::Value, key: &[u8]) -> Result<String, String> {
    let mut nonce = jarvis_home_common::random::<12>()?;
    let abc = b"ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678";
    for byte in &mut nonce {
        *byte = abc[*byte as usize % abc.len()];
    }
    let cipher = seal(
        key,
        nonce,
        &serde_json::to_vec(data).map_err(|_| "Неверные данные Tuya")?,
        &[],
    )?;
    Ok(format!("{}{}", B64.encode(nonce), B64.encode(cipher)))
}
pub fn decrypt(data: &str, key: &[u8]) -> Result<serde_json::Value, String> {
    let raw = B64.decode(data).map_err(|_| "Неверный ответ Tuya")?;
    if raw.len() < 28 {
        return Err("Короткий ответ Tuya".into());
    }
    serde_json::from_slice(&open(key, raw[..12].try_into().unwrap(), &raw[12..], &[])?)
        .map_err(|_| "Неверный JSON Tuya".into())
}
