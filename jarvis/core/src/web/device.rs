use super::{App,bad,ok,reply};
use axum::{extract::State,body::Bytes,http::StatusCode,response::Response,Json};
use serde::Deserialize;
use serde_json::json;
use std::{path::Path,time::Duration};
use crate::led::Mode;
use crate::wake_profiles::{TUNED_MODEL, TUNED_PREV};

#[derive(Default,Deserialize)]#[serde(default)]
pub struct Preview{#[serde(alias="Mode")]mode:String,#[serde(alias="Color")]color:String,#[serde(alias="Brightness")]brightness:i64,#[serde(alias="Seconds")]seconds:f64}
pub async fn leds(State(app):State<App>,Json(input):Json<Preview>)->Response{
    if !app.engine.leds.available(){return bad("у этой колонки нет подсветки, которой может управлять Джарвис");}
    let mode=match input.mode.as_str(){"off"=>{app.engine.leds.set(Mode::Off);return ok();},"listening"=>Mode::Listening,"thinking"=>Mode::Thinking,"speaking"=>Mode::Speaking,_=>return bad("неизвестный режим")};
    let palette=app.store.with(|s|s.led_for());let color=if input.color.len()==7 && input.color.starts_with('#') && input.color.as_bytes()[1..].iter().all(u8::is_ascii_hexdigit){input.color}else{match mode{Mode::Listening=>palette.listening,Mode::Thinking=>palette.thinking,_=>palette.speaking}};
    let brightness=if input.brightness>0{input.brightness.min(100)}else{palette.brightness};
    let seconds=if input.seconds.is_finite() && input.seconds>0.0{input.seconds.clamp(0.1,30.0)}else{3.0};
    app.engine.leds.show(mode,&color,brightness,Duration::from_secs_f64(seconds));ok()
}
fn validate(bytes:&[u8])->Result<(),String>{jarvis_dsp::kws::Kws::load(bytes,Box::new(||5)).map(|_|()).map_err(|e|format!("Это не модель слова: {e}"))}
fn replace(data:&Path,new:Option<&[u8]>)->Result<(),String>{
    let path=data.join(TUNED_MODEL);let previous=data.join(TUNED_PREV);
    let current=std::fs::read(&path).or_else(|_|std::fs::read(data.join("wake.bin"))).ok();
    let bytes=match new{Some(bytes)=>bytes.to_vec(),None=>std::fs::read(&previous).map_err(|_|"Прежней модели нет")?};validate(&bytes)?;
    std::fs::create_dir_all(path.parent().unwrap()).map_err(|e|e.to_string())?;
    if let Some(current)=&current{crate::settings::write_atomic(&previous,current,0o644).map_err(|e|e.to_string())?;}
    crate::settings::write_atomic(&path,&bytes,0o644).map_err(|e|e.to_string())?;Ok(())
}
async fn apply(app:App,new:Option<Bytes>)->Response{
    let _lock=app.wake_update.lock().await;let path=app.data_dir.clone();
    if !app.plugins.wake_training(){return bad(super::wake_profiles::DISABLED);}
    match tokio::task::spawn_blocking(move||replace(&path,new.as_deref())).await{
        Ok(Ok(()))=>{app.engine.listener.request_reload();log::info!("слово активации: модель обновлена без перезапуска ядра");reply(StatusCode::OK,json!({"ok":true,"reloaded":true}))},
        Ok(Err(error))=>bad(error),Err(_)=>bad("Не удалось обновить модель"),
    }
}
pub async fn model(State(app):State<App>,body:Bytes)->Response{if body.is_empty(){return bad("Пустой файл");}apply(app,Some(body)).await}
pub async fn restore(State(app):State<App>)->Response{apply(app,None).await}

#[cfg(test)]mod tests{
    use super::*;
    #[test]fn valid_model_can_be_installed_and_restored(){
        let original=include_bytes!("../../../../models/wake.bin");let mut next=original.to_vec();let end=next.len();let weight=f32::from_le_bytes(next[end-4..].try_into().unwrap());next[end-4..].copy_from_slice(&(weight+0.125).to_le_bytes());
        let dir=std::env::temp_dir().join(format!("jarvis-wake-{}",crate::settings::rand_hex(8)));std::fs::create_dir_all(&dir).unwrap();std::fs::write(dir.join("wake.bin"),original).unwrap();
        replace(&dir,Some(&next)).unwrap();assert_eq!(std::fs::read(dir.join(TUNED_MODEL)).unwrap(),next);assert_eq!(std::fs::read(dir.join(TUNED_PREV)).unwrap(),original);replace(&dir,None).unwrap();assert_eq!(std::fs::read(dir.join(TUNED_MODEL)).unwrap(),original);assert_eq!(std::fs::read(dir.join(TUNED_PREV)).unwrap(),next);assert_eq!(std::fs::read(dir.join("wake.bin")).unwrap(),original);std::fs::remove_dir_all(dir).unwrap();
    }
    #[test]fn invalid_model_never_replaces_active_or_previous_model(){
        let dir=std::env::temp_dir().join(format!("jarvis-wake-{}",crate::settings::rand_hex(8)));std::fs::create_dir_all(&dir).unwrap();
        std::fs::create_dir_all(dir.join(TUNED_MODEL).parent().unwrap()).unwrap();std::fs::write(dir.join(TUNED_MODEL),b"current").unwrap();std::fs::write(dir.join(TUNED_PREV),b"previous").unwrap();
        assert!(replace(&dir,Some(b"invalid")).is_err());assert!(replace(&dir,None).is_err());
        assert_eq!(std::fs::read(dir.join(TUNED_MODEL)).unwrap(),b"current");assert_eq!(std::fs::read(dir.join(TUNED_PREV)).unwrap(),b"previous");std::fs::remove_dir_all(dir).unwrap();
    }
}
