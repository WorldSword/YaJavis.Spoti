use crate::{config::Config, protocol::BoxIo};
use futures_util::{Sink, Stream};
use std::{
    io,
    pin::Pin,
    task::{Context, Poll},
};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_tungstenite::{
    WebSocketStream,
    tungstenite::{self, Message, client::IntoClientRequest, protocol::WebSocketConfig},
};

pub(crate) struct WsIo {
    socket: WebSocketStream<BoxIo>,
    bytes: tungstenite::Bytes,
    consumed: usize,
    eof: bool,
}
fn error(e: tungstenite::Error) -> io::Error {
    io::Error::other(e)
}
pub(crate) async fn connect(io: BoxIo, config: &Config) -> io::Result<WsIo> {
    let scheme = if config.tls { "wss" } else { "ws" };
    let uri = format!("{scheme}://{}{path}", config.ws_host, path = config.ws_path);
    let request = uri.into_client_request().map_err(error)?;
    let limits = WebSocketConfig::default()
        .write_buffer_size(16 * 1024)
        .max_write_buffer_size(64 * 1024)
        .max_message_size(Some(1024 * 1024))
        .max_frame_size(Some(1024 * 1024));
    // The connected stream always goes to config.host/config.port; Host never causes a second dial.
    let (socket, _) = tokio_tungstenite::client_async_with_config(request, io, Some(limits))
        .await
        .map_err(error)?;
    Ok(WsIo {
        socket,
        bytes: Default::default(),
        consumed: 0,
        eof: false,
    })
}
impl AsyncRead for WsIo {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        if out.remaining() == 0 {
            return Poll::Ready(Ok(()));
        }
        // Bound control/empty frames handled in one poll, so a busy peer cannot monopolize a worker.
        for _ in 0..32 {
            if me.consumed < me.bytes.len() {
                let n = out.remaining().min(me.bytes.len() - me.consumed);
                out.put_slice(&me.bytes[me.consumed..me.consumed + n]);
                me.consumed += n;
                return Poll::Ready(Ok(()));
            }
            me.bytes = Default::default();
            me.consumed = 0;
            if me.eof {
                return Poll::Ready(Ok(()));
            }
            match std::task::ready!(Pin::new(&mut me.socket).poll_next(cx)) {
                Some(Ok(Message::Binary(bytes))) => me.bytes = bytes,
                Some(Ok(Message::Close(_))) | None => me.eof = true,
                Some(Ok(Message::Ping(_) | Message::Pong(_))) => {}
                Some(Ok(_)) => {
                    return Poll::Ready(Err(io::Error::other("non-binary VLESS WebSocket frame")));
                }
                Some(Err(tungstenite::Error::ConnectionClosed)) => me.eof = true,
                Some(Err(e)) => return Poll::Ready(Err(error(e))),
            }
        }
        cx.waker().wake_by_ref();
        Poll::Pending
    }
}
impl AsyncWrite for WsIo {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        data: &[u8],
    ) -> Poll<io::Result<usize>> {
        let me = self.get_mut();
        if data.is_empty() {
            return Poll::Ready(Ok(0));
        }
        std::task::ready!(Pin::new(&mut me.socket).poll_ready(cx)).map_err(error)?;
        let n = data.len().min(16 * 1024);
        Pin::new(&mut me.socket)
            .start_send(Message::binary(data[..n].to_vec()))
            .map_err(error)?;
        Poll::Ready(Ok(n))
    }
    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().socket)
            .poll_flush(cx)
            .map_err(error)
    }
    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().socket)
            .poll_close(cx)
            .map_err(error)
    }
}
