use super::{App,reply};
use axum::{extract::{Query,State},http::StatusCode,response::Response};
use serde_json::json;
use std::{collections::HashMap,io::{Read,Seek,SeekFrom},path::Path};

pub async fn stats(State(app):State<App>,Query(query):Query<HashMap<String,String>>)->Response{
    let seconds=query.get("range").and_then(|n|n.parse::<i64>().ok()).unwrap_or(900);
    reply(StatusCode::OK,json!({"points":app.telemetry.since(seconds),"every":crate::telemetry::EVERY}))
}
pub async fn logs(State(app):State<App>,Query(query):Query<HashMap<String,String>>)->Response{
    let source=match query.get("src").map(String::as_str){Some("alice")=>"alice",Some("media")=>"media",_=>"jarvis"};
    let path=match source{"alice"=>Path::new("/data/quasar/daemons_logs/maind.log").to_path_buf(),"media"=>Path::new("/data/quasar/daemons_logs/mediad.log").to_path_buf(),_=>{
        let standard=Path::new("/tmp/jarvisd/jarvisd.log");if standard.exists(){standard.to_path_buf()}else{app.data_dir.join("jarvisd.log")}
    }};
    let n=query.get("n").and_then(|n|n.parse::<usize>().ok()).filter(|n|(1..=2000).contains(n)).unwrap_or(400);
    let filter=query.get("q").map(|q|q.trim().to_lowercase()).unwrap_or_default();
    if filter.len()>4096{return super::bad("Слишком длинный фильтр");}
    match tokio::task::spawn_blocking(move||tail(&path,n,&filter)).await{
        Ok(Ok(lines))=>reply(StatusCode::OK,json!({"src":source,"lines":lines})),
        _=>reply(StatusCode::OK,json!({"src":source,"lines":[],"error":"журнала пока нет"})),
    }
}
fn tail(path:&Path,n:usize,filter:&str)->std::io::Result<Vec<String>>{
    let mut file=std::fs::File::open(path)?;let size=file.metadata()?.len();
    let window=if filter.is_empty(){(n as u64*300).clamp(64<<10,1<<20)}else{1<<20};
    let offset=size.saturating_sub(window);file.seek(SeekFrom::Start(offset))?;
    let mut bytes=Vec::new();file.take(window).read_to_end(&mut bytes)?;
    if offset>0{if let Some(end)=bytes.iter().position(|b|*b==b'\n'){bytes.drain(..=end);}else{bytes.clear();}}
    let text=String::from_utf8_lossy(&bytes);let mut lines:Vec<String>=text.lines().rev().filter(|line|filter.is_empty()||line.to_lowercase().contains(filter)).take(n).map(|line|{
        if line.len()<=1200{line.to_string()}else{let mut end=1200;while !line.is_char_boundary(end){end-=1;}format!("{}…",&line[..end])}
    }).collect();lines.reverse();Ok(lines)
}
#[cfg(test)]mod tests{
    use super::*;
    #[test]fn log_tail_filters_caps_bytes_and_preserves_utf8(){
        let path=std::env::temp_dir().join(format!("jarvis-logs-{}",crate::settings::rand_hex(8)));
        std::fs::write(&path,format!("old\nОшибка первая\nOK\nошибка {}\n", "я".repeat(900))).unwrap();
        let lines=tail(&path,2,"ошибка").unwrap();assert_eq!(lines[0],"Ошибка первая");assert!(lines[1].ends_with('…'));assert!(lines[1].len()<=1203);
        assert_eq!(tail(&path,1,"").unwrap().len(),1);
        std::fs::write(&path,format!("{}\nlast\n","x".repeat(2<<20))).unwrap();assert_eq!(tail(&path,400,"").unwrap(),vec!["last"]);
        std::fs::remove_file(path).unwrap();
    }
}
