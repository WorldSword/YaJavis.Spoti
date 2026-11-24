//! A TLS record is read exactly once and never together with the next record.
//! Vision can therefore switch either direction to raw TCP without losing buffered wire bytes.
use crate::protocol::BoxIo;
use reality_tls as tls;
use std::{
    io::{self, Read, Write},
    pin::Pin,
    task::{Context, Poll},
};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

pub(crate) struct TlsIo {
    io: BoxIo,
    session: tls::Connection,
    record: Vec<u8>,
    filled: usize,
    output: Vec<u8>,
    written: usize,
    pub read_raw: bool,
    pub write_raw: bool,
    closed: bool,
    failed: bool,
}
impl TlsIo {
    pub fn new(io: BoxIo, session: impl Into<tls::Connection>) -> Self {
        let mut session = session.into();
        session.set_buffer_limit(Some(32 * 1024));
        Self {
            io,
            session,
            record: vec![0; 5],
            filled: 0,
            output: Vec::new(),
            written: 0,
            read_raw: false,
            write_raw: false,
            closed: false,
            failed: false,
        }
    }
    pub async fn handshake(&mut self) -> io::Result<()> {
        std::future::poll_fn(|cx| {
            for _ in 0..32 {
                std::task::ready!(self.drain(cx))?;
                if !self.session.is_handshaking() {
                    return Poll::Ready(Ok(()));
                }
                if !std::task::ready!(self.read_record(cx))? {
                    return Poll::Ready(Err(io::ErrorKind::UnexpectedEof.into()));
                }
            }
            cx.waker().wake_by_ref();
            Poll::Pending
        })
        .await
    }
    fn read_record(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<bool>> {
        if self.failed {
            return Poll::Ready(Err(io::Error::other("failed VPN TLS connection")));
        }
        loop {
            if self.filled == self.record.len() {
                if self.record.len() == 5 {
                    let length = u16::from_be_bytes([self.record[3], self.record[4]]) as usize;
                    if length > 18432 {
                        self.failed = true;
                        return Poll::Ready(Err(io::Error::other("oversized VPN TLS record")));
                    }
                    self.record.resize(5 + length, 0);
                }
                if self.filled == self.record.len() {
                    break;
                }
            }
            let mut buf = ReadBuf::new(&mut self.record[self.filled..]);
            std::task::ready!(Pin::new(&mut self.io).poll_read(cx, &mut buf))?;
            let n = buf.filled().len();
            if n == 0 {
                if self.filled != 0 {
                    self.failed = true;
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::UnexpectedEof,
                        "truncated VPN TLS record",
                    )));
                }
                self.session.read_tls(&mut io::empty())?;
                return Poll::Ready(Ok(false));
            }
            self.filled += n;
        }
        let mut input = self.record.as_slice();
        while !input.is_empty() {
            let n = self.session.read_tls(&mut input)?;
            if n == 0 {
                self.failed = true;
                return Poll::Ready(Err(io::Error::other("VPN TLS did not consume record")));
            }
        }
        if self.session.process_new_packets().is_err() {
            // Never send alerts or expose application data after failed REALITY authentication.
            self.failed = true;
            self.output.clear();
            self.written = 0;
            return Poll::Ready(Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "VPN TLS authentication failed",
            )));
        }
        self.record.truncate(5);
        self.filled = 0;
        Poll::Ready(Ok(true))
    }
    pub fn drain(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.failed {
            return Poll::Ready(Err(io::Error::other("failed VPN TLS connection")));
        }
        if self.write_raw {
            return Pin::new(&mut self.io).poll_flush(cx);
        }
        loop {
            while self.written < self.output.len() {
                let n = std::task::ready!(
                    Pin::new(&mut self.io).poll_write(cx, &self.output[self.written..])
                )?;
                if n == 0 {
                    return Poll::Ready(Err(io::ErrorKind::WriteZero.into()));
                }
                self.written += n;
            }
            self.output.clear();
            self.written = 0;
            if !self.session.wants_write() {
                return Pin::new(&mut self.io).poll_flush(cx);
            }
            self.session.write_tls(&mut self.output)?;
        }
    }
}
impl AsyncRead for TlsIo {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        if out.remaining() == 0 {
            return Poll::Ready(Ok(()));
        }
        if me.failed || me.session.is_handshaking() {
            return Poll::Ready(Err(io::Error::other("VPN TLS is not authenticated")));
        }
        for _ in 0..32 {
            match me.session.reader().read(out.initialize_unfilled()) {
                Ok(n) if n > 0 => {
                    out.advance(n);
                    return Poll::Ready(Ok(()));
                }
                Ok(0) if !me.read_raw => return Poll::Ready(Ok(())),
                Ok(_) => {}
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => {}
                Err(e) => return Poll::Ready(Err(e)),
            }
            if me.read_raw {
                return Pin::new(&mut me.io).poll_read(cx, out);
            }
            // Flush KeyUpdate acknowledgements before waiting for another TLS record.
            if let Poll::Ready(Err(e)) = me.drain(cx) {
                return Poll::Ready(Err(e));
            }
            if !std::task::ready!(me.read_record(cx))? {
                // rustls distinguishes a valid close_notify from truncated TLS.
                match me.session.reader().read(out.initialize_unfilled()) {
                    Ok(n) => {
                        out.advance(n);
                        return Poll::Ready(Ok(()));
                    }
                    Err(e) => return Poll::Ready(Err(e)),
                }
            }
        }
        cx.waker().wake_by_ref();
        Poll::Pending
    }
}
impl AsyncWrite for TlsIo {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        data: &[u8],
    ) -> Poll<io::Result<usize>> {
        let me = self.get_mut();
        if me.failed || me.closed || me.session.is_handshaking() {
            return Poll::Ready(Err(io::Error::other("VPN TLS is not writable")));
        }
        std::task::ready!(me.drain(cx))?;
        if me.write_raw {
            return Pin::new(&mut me.io).poll_write(cx, data);
        }
        Poll::Ready(
            me.session
                .writer()
                .write(&data[..data.len().min(16 * 1024)]),
        )
    }
    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        self.get_mut().drain(cx)
    }
    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        if !me.closed {
            if !me.write_raw {
                me.session.send_close_notify();
            }
            me.closed = true;
        }
        std::task::ready!(me.drain(cx))?;
        Pin::new(&mut me.io).poll_shutdown(cx)
    }
}
