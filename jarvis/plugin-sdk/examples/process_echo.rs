use jarvis_plugin_sdk::{Plugin, Response, Value, json, process};

struct Echo;

impl Plugin for Echo {
    fn request(&self, method: &str, params: Value) -> Response<'_> {
        let response = match method {
            "command" if params == "раст эхо" => json!("Rust-пакет отвечает"),
            "llm.chat" => params["messages"]
                .as_array()
                .and_then(|messages| messages.last())
                .map(|message| message["content"].clone())
                .unwrap_or_default(),
            _ => Value::Null,
        };
        Box::pin(async move { Ok(response) })
    }
}

#[tokio::main(flavor = "current_thread")]
async fn main() -> std::io::Result<()> {
    process::serve(|_| Ok(Box::new(Echo))).await
}
