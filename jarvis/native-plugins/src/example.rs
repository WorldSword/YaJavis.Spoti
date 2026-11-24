use super::{Plugin, Registration, Response, Value, json};

pub const PLUGIN: Registration = Registration {
    manifest: r#"{"id":"native-echo","name":"Rust Echo","version":"1.0.0","kind":"module","category":"assistant","main":"native-echo","provides":["native-echo"],"api":1}"#,
    chat: true,
    start: |_| Ok(Box::new(Echo)),
};

struct Echo;

impl Plugin for Echo {
    fn request(&self, method: &str, params: Value) -> Response<'_> {
        let action = method.to_owned();
        Box::pin(async move {
            match action.as_str() {
                "llm.chat" => params["messages"]
                    .as_array()
                    .and_then(|items| items.last())
                    .and_then(|last| last["content"].as_str())
                    .map(|text| json!(format!("Rust: {text}")))
                    .ok_or("Ожидается сообщение".into()),
                "command" if params == "раст эхо" || params == "rust echo" => {
                    Ok(json!("Rust Echo"))
                }
                _ => Ok(Value::Null),
            }
        })
    }
}
