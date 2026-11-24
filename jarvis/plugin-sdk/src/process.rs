//! Framed JSON for independently installed Rust executables.
pub mod rpc;
use super::{Map, Plugin, Value, json};
use std::io;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

pub const API: u64 = 1;
const MAX_FRAME: usize = 4 << 20;

pub async fn read_frame(reader: &mut (impl AsyncRead + Unpin)) -> io::Result<Value> {
    let bytes = reader.read_u32_le().await? as usize;
    if bytes == 0 || bytes > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "Invalid plugin frame length",
        ));
    }
    let mut data = vec![0; bytes];
    reader.read_exact(&mut data).await?;
    serde_json::from_slice(&data).map_err(io::Error::other)
}

pub async fn write_frame(writer: &mut (impl AsyncWrite + Unpin), value: &Value) -> io::Result<()> {
    let data = serde_json::to_vec(value).map_err(io::Error::other)?;
    if data.len() > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "Plugin frame is too large",
        ));
    }
    writer.write_u32_le(data.len() as u32).await?;
    writer.write_all(&data).await?;
    writer.flush().await
}

pub struct Init {
    pub settings: Map<String, Value>,
    pub info: Value,
}

/// Runs the plugin over stdin/stdout. The current directory holds its data.
/// Use stderr for diagnostics and stdout only for the protocol.
pub async fn serve(start: impl FnOnce(Init) -> Result<Box<dyn Plugin>, String>) -> io::Result<()> {
    let mut input = tokio::io::stdin();
    let mut output = tokio::io::stdout();
    let init = read_frame(&mut input).await?;
    if init["method"] != "init" || init["params"]["api"] != API {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "Unsupported plugin protocol",
        ));
    }
    let result = start(Init {
        settings: init["params"]["settings"]
            .as_object()
            .cloned()
            .unwrap_or_default(),
        info: init["params"]["info"].clone(),
    });
    let plugin = match result {
        Ok(plugin) => plugin,
        Err(message) => {
            write_frame(&mut output, &json!({"error": message})).await?;
            return Ok(());
        }
    };
    let result = async {
        write_frame(
            &mut output,
            &json!({"result": {"api": API, "tools": plugin.tools()}}),
        )
        .await?;
        loop {
            let call = read_frame(&mut input).await?;
            let method = call["method"].as_str().unwrap_or("");
            let params = call["params"].clone();
            let result = match method {
                "shutdown" => break Ok(()),
                "configure" => {
                    plugin.configure(params.as_object().cloned().unwrap_or_default());
                    Ok(Value::Null)
                }
                _ => plugin.request(method, params).await,
            };
            let reply = match result {
                Ok(value) => json!({"result": value}),
                Err(error) => json!({"error": error}),
            };
            write_frame(&mut output, &reply).await?;
        }
    }
    .await;
    plugin.stop();
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn roundtrip_and_reject_oversized_frame() {
        let (mut writer, mut reader) = tokio::io::duplex(256);
        let value = json!({"method": "init", "params": {"api": API}});
        write_frame(&mut writer, &value).await.unwrap();
        assert_eq!(read_frame(&mut reader).await.unwrap(), value);
        writer.write_u32_le((MAX_FRAME + 1) as u32).await.unwrap();
        assert_eq!(
            read_frame(&mut reader).await.unwrap_err().kind(),
            io::ErrorKind::InvalidData
        );
    }
}
