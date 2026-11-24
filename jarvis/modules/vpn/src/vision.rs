//! Client-side XTLS Vision framing. Read/write mode changes are independent.
use crate::tls_io::TlsIo;
use std::{
    io,
    pin::Pin,
    task::{Context, Poll},
};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

pub(crate) struct VisionIo {
    tls: TlsIo,
    uuid: [u8; 16],
    response: [u8; 2],
    response_at: usize,
    response_skip: usize,
    prefix: Vec<u8>,
    prefix_at: usize,
    header: [u8; 5],
    header_at: usize,
    content: usize,
    padding: usize,
    command: u8,
    read_done: bool,
    pending: Vec<u8>,
    pending_at: usize,
    pending_switch: u8,
    first_write: bool,
    write_done: bool,
    writes_left: u8,
    is_tls: bool,
    scan: HelloScanner,
    failed: bool,
}
fn invalid() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "invalid Vision frame")
}
impl VisionIo {
    pub fn new(tls: TlsIo, uuid: [u8; 16]) -> Self {
        Self {
            tls,
            uuid,
            response: [0; 2],
            response_at: 0,
            response_skip: 0,
            prefix: vec![0; 16],
            prefix_at: 0,
            header: [0; 5],
            header_at: 0,
            content: 0,
            padding: 0,
            command: 0,
            read_done: false,
            pending: Vec::new(),
            pending_at: 0,
            pending_switch: 0,
            first_write: true,
            write_done: false,
            writes_left: 8,
            is_tls: false,
            scan: HelloScanner::default(),
            failed: false,
        }
    }
    fn drain(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        while self.pending_at < self.pending.len() {
            let n = std::task::ready!(
                Pin::new(&mut self.tls).poll_write(cx, &self.pending[self.pending_at..])
            )?;
            if n == 0 {
                return Poll::Ready(Err(io::ErrorKind::WriteZero.into()));
            }
            self.pending_at += n;
        }
        if self.pending_switch != 0 {
            std::task::ready!(Pin::new(&mut self.tls).poll_flush(cx))?;
            self.write_done = true;
            if self.pending_switch == 2 {
                self.tls.write_raw = true;
            }
            self.pending_switch = 0;
        }
        self.pending.clear();
        self.pending_at = 0;
        Poll::Ready(Ok(()))
    }
    fn read_some(
        tls: &mut TlsIo,
        cx: &mut Context<'_>,
        slice: &mut [u8],
    ) -> Poll<io::Result<usize>> {
        let mut buf = ReadBuf::new(slice);
        std::task::ready!(Pin::new(tls).poll_read(cx, &mut buf))?;
        Poll::Ready(Ok(buf.filled().len()))
    }
    fn read_exact_part(
        tls: &mut TlsIo,
        cx: &mut Context<'_>,
        slice: &mut [u8],
        at: &mut usize,
    ) -> Poll<io::Result<()>> {
        while *at < slice.len() {
            let n = std::task::ready!(Self::read_some(tls, cx, &mut slice[*at..]))?;
            if n == 0 {
                return Poll::Ready(Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "truncated Vision header",
                )));
            }
            *at += n;
        }
        Poll::Ready(Ok(()))
    }
    fn read_inner(&mut self, cx: &mut Context<'_>, out: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        std::task::ready!(Self::read_exact_part(
            &mut self.tls,
            cx,
            &mut self.response,
            &mut self.response_at
        ))?;
        if self.response[0] != 0 {
            return Poll::Ready(Err(invalid()));
        }
        if self.response_at == 2 {
            self.response_skip = self.response[1] as usize;
            self.response_at = 3;
        }
        let mut discard = [0; 255];
        while self.response_skip > 0 {
            let n = std::task::ready!(Self::read_some(
                &mut self.tls,
                cx,
                &mut discard[..self.response_skip]
            ))?;
            if n == 0 {
                return Poll::Ready(Err(invalid()));
            }
            self.response_skip -= n;
        }
        if self.read_done {
            return Pin::new(&mut self.tls).poll_read(cx, out);
        }
        std::task::ready!(Self::read_exact_part(
            &mut self.tls,
            cx,
            &mut self.prefix,
            &mut self.prefix_at
        ))?;
        if self.prefix != self.uuid {
            return Poll::Ready(Err(invalid()));
        }
        for _ in 0..32 {
            if self.header_at < 5 {
                let n = std::task::ready!(Self::read_some(
                    &mut self.tls,
                    cx,
                    &mut self.header[self.header_at..]
                ))?;
                if n == 0 {
                    if self.header_at == 0 {
                        return Poll::Ready(Ok(()));
                    }
                    return Poll::Ready(Err(invalid()));
                }
                self.header_at += n;
                if self.header_at < 5 {
                    continue;
                }
                self.command = self.header[0];
                if self.command > 2 {
                    return Poll::Ready(Err(invalid()));
                }
                self.content = u16::from_be_bytes([self.header[1], self.header[2]]) as usize;
                self.padding = u16::from_be_bytes([self.header[3], self.header[4]]) as usize;
                if self.content + self.padding > 65535 {
                    return Poll::Ready(Err(invalid()));
                }
            }
            if self.content > 0 {
                let limit = out.remaining().min(self.content);
                let n = std::task::ready!(Self::read_some(
                    &mut self.tls,
                    cx,
                    &mut out.initialize_unfilled()[..limit]
                ))?;
                if n == 0 {
                    return Poll::Ready(Err(invalid()));
                }
                self.scan.feed(&out.initialize_unfilled()[..n]);
                out.advance(n);
                self.content -= n;
                return Poll::Ready(Ok(()));
            }
            while self.padding > 0 {
                let limit = self.padding.min(discard.len());
                let n =
                    std::task::ready!(Self::read_some(&mut self.tls, cx, &mut discard[..limit]))?;
                if n == 0 {
                    return Poll::Ready(Err(invalid()));
                }
                self.padding -= n;
            }
            self.header_at = 0;
            if self.command != 0 {
                self.read_done = true;
                if self.command == 2 {
                    self.tls.read_raw = true;
                }
                return Pin::new(&mut self.tls).poll_read(cx, out);
            }
        }
        cx.waker().wake_by_ref();
        Poll::Pending
    }
}
impl AsyncRead for VisionIo {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        if out.remaining() == 0 {
            return Poll::Ready(Ok(()));
        }
        if me.failed {
            return Poll::Ready(Err(invalid()));
        }
        let result = me.read_inner(cx, out);
        if matches!(result, Poll::Ready(Err(_))) {
            me.failed = true;
        }
        result
    }
}
impl AsyncWrite for VisionIo {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        data: &[u8],
    ) -> Poll<io::Result<usize>> {
        let me = self.get_mut();
        if data.is_empty() {
            return Poll::Ready(Ok(0));
        }
        if me.failed {
            return Poll::Ready(Err(invalid()));
        }
        std::task::ready!(me.drain(cx))?;
        if me.write_done {
            return Pin::new(&mut me.tls).poll_write(cx, data);
        }
        let n = data.len().min(8171);
        let data = &data[..n];
        me.writes_left = me.writes_left.saturating_sub(1);
        if me.first_write
            && data.len() >= 6
            && data[0] == 22
            && data[1] == 3
            && matches!(data[2], 1..=3)
            && data[5] == 1
        {
            me.is_tls = true;
        }
        let direct = me.is_tls
            && me.scan.tls13
            && data.len() >= 5
            && data[..3] == [23, 3, 3]
            && (1..=18432).contains(&u16::from_be_bytes([data[3], data[4]]));
        let command = if direct {
            2
        } else if me.writes_left == 0 {
            1
        } else {
            0
        };
        let mut random = [0; 2];
        getrandom::fill(&mut random)
            .map_err(|_| io::Error::other("Vision padding randomness failed"))?;
        let random = u16::from_be_bytes(random) as usize;
        let padding = if me.is_tls && n < 900 {
            900 - n + random % 500
        } else {
            random % 256
        };
        let padding = padding.min(8171 - n);
        if me.first_write {
            me.pending.extend(me.uuid);
            me.first_write = false;
        }
        me.pending.push(command);
        me.pending.extend((n as u16).to_be_bytes());
        me.pending.extend((padding as u16).to_be_bytes());
        me.pending.extend(data);
        let total = me.pending.len() + padding;
        me.pending.resize(total, 0);
        me.pending_switch = command;
        Poll::Ready(Ok(n))
    }
    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        std::task::ready!(me.drain(cx))?;
        Pin::new(&mut me.tls).poll_flush(cx)
    }
    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        std::task::ready!(me.drain(cx))?;
        Pin::new(&mut me.tls).poll_shutdown(cx)
    }
}

/// Passive, bounded parsing of the target's ServerHello, used only to enable outbound DIRECT.
#[derive(Default)]
struct HelloScanner {
    wire: Vec<u8>,
    handshake: Vec<u8>,
    stopped: bool,
    tls13: bool,
}
impl HelloScanner {
    fn feed(&mut self, bytes: &[u8]) {
        if self.stopped {
            return;
        }
        if self.wire.len() + bytes.len() > 32768 {
            self.stopped = true;
            return;
        }
        self.wire.extend(bytes);
        loop {
            if self.wire.len() < 5 {
                return;
            }
            if self.wire[0] != 22 || self.wire[1] != 3 {
                self.stopped = true;
                return;
            }
            let length = u16::from_be_bytes([self.wire[3], self.wire[4]]) as usize;
            if length > 18432 {
                self.stopped = true;
                return;
            }
            if self.wire.len() < 5 + length {
                return;
            }
            if self.handshake.len() + length > 32768 {
                self.stopped = true;
                return;
            }
            self.handshake.extend(&self.wire[5..5 + length]);
            self.wire.drain(..5 + length);
            if self.handshake.len() >= 4 {
                if self.handshake[0] != 2 {
                    self.stopped = true;
                    return;
                }
                let size = ((self.handshake[1] as usize) << 16)
                    | ((self.handshake[2] as usize) << 8)
                    | self.handshake[3] as usize;
                if size > 32764 {
                    self.stopped = true;
                    return;
                }
                if self.handshake.len() < size + 4 {
                    continue;
                }
                self.tls13 = server_hello_tls13(&self.handshake[4..4 + size]);
                self.stopped = true;
                self.wire.clear();
                self.handshake.clear();
                return;
            }
        }
    }
}
fn server_hello_tls13(hello: &[u8]) -> bool {
    let Some(&session_len) = hello.get(34) else {
        return false;
    };
    let at = 35 + session_len as usize;
    let Some(fixed) = hello.get(at..at + 5) else {
        return false;
    };
    if !matches!(u16::from_be_bytes([fixed[0], fixed[1]]), 0x1301..=0x1303) || fixed[2] != 0 {
        return false;
    }
    let length = u16::from_be_bytes([fixed[3], fixed[4]]) as usize;
    let Some(mut extensions) = hello.get(at + 5..at + 5 + length) else {
        return false;
    };
    while extensions.len() >= 4 {
        let kind = u16::from_be_bytes([extensions[0], extensions[1]]);
        let n = u16::from_be_bytes([extensions[2], extensions[3]]) as usize;
        let Some(value) = extensions.get(4..4 + n) else {
            return false;
        };
        if kind == 43 {
            return value == [3, 4];
        }
        extensions = &extensions[4 + n..];
    }
    false
}
