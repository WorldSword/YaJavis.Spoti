use super::{App,bad,ok};
use axum::{extract::State,response::Response};
use serde_json::{Value,json};
use std::time::Duration;
pub async fn login(State(app):State<App>)->Response{
    let provider=match app.plugins.provider("chatgpt"){Ok(p)=>p,Err(e)=>return bad(e)};
    match provider.request("login.start",Value::Null,Duration::from_secs(25)).await{
        Ok(state)=>super::reply(axum::http::StatusCode::OK,json!({"ok":true,"login":state})),Err(e)=>bad(e),
    }
}
pub async fn logout(State(app):State<App>)->Response{
    if let Ok(provider)=app.plugins.provider("chatgpt") {
        if let Err(e)=provider.request("auth.logout",Value::Null,Duration::from_secs(5)).await{return bad(e);}
    }else if let Err(e)=app.store.update(|s|->Result<(),std::io::Error>{s.chatgpt=json!({});Ok(())}){return bad(e.to_string());}
    ok()
}
