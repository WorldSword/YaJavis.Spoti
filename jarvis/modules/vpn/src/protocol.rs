use std::{
    io,
    net::IpAddr,
    pin::Pin,
    task::{Context, Poll},
};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, ReadBuf};

pub(crate) trait Io: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Io for T {}
pub(crate) type BoxIo = Box<dyn Io>;

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub(crate) enum Address {
    Ip(IpAddr),
    Domain(Vec<u8>),
}
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub(crate) struct Target {
    pub address: Address,
    pub port: u16,
}
impl Target {
    pub fn vless_header(&self, uuid: &[u8; 16]) -> io::Result<Vec<u8>> {
        self.vless_header_for(uuid, false)
    }
    pub fn vless_header_for(&self, uuid: &[u8; 16], udp: bool) -> io::Result<Vec<u8>> {
        let mut out = Vec::with_capacity(280);
        out.push(0);
        out.extend(uuid);
        out.extend([0, if udp { 2 } else { 1 }]); // no addons, UDP or TCP
        self.write_vless_endpoint(&mut out)?;
        Ok(out)
    }

    pub fn write_vless_endpoint(&self, out: &mut Vec<u8>) -> io::Result<()> {
        if self.port == 0 {
            return Err(io::Error::other("zero target port"));
        }
        out.extend(self.port.to_be_bytes());
        match &self.address {
            Address::Ip(IpAddr::V4(ip)) => {
                out.push(1);
                out.extend(ip.octets());
            }
            Address::Ip(IpAddr::V6(ip)) => {
                out.push(3);
                out.extend(ip.octets());
            }
            Address::Domain(domain) if !domain.is_empty() && domain.len() <= 255 => {
                out.extend([2, domain.len() as u8]);
                out.extend(domain);
            }
            _ => return Err(io::Error::other("invalid target domain")),
        }
        Ok(())
    }
}

pub(crate) enum Request {
    Connect(Target),
    Associate(Target),
}

pub(crate) async fn socks_handshake(
    io: &mut (impl AsyncRead + AsyncWrite + Unpin),
    user: &[u8],
    pass: &[u8],
) -> io::Result<Request> {
    let mut head = [0; 2];
    io.read_exact(&mut head).await?;
    if head[0] != 5 {
        return Err(io::Error::other("SOCKS version"));
    }
    let mut methods = vec![0; head[1] as usize];
    io.read_exact(&mut methods).await?;
    if !methods.contains(&2) {
        io.write_all(&[5, 255]).await?;
        return Err(io::Error::other("SOCKS authentication required"));
    }
    io.write_all(&[5, 2]).await?;
    io.read_exact(&mut head).await?;
    if head[0] != 1 {
        return Err(io::Error::other("SOCKS auth version"));
    }
    let mut supplied_user = vec![0; head[1] as usize];
    io.read_exact(&mut supplied_user).await?;
    let mut supplied_pass = vec![0; io.read_u8().await? as usize];
    io.read_exact(&mut supplied_pass).await?;
    if !constant_eq(&supplied_user, user) || !constant_eq(&supplied_pass, pass) {
        io.write_all(&[1, 1]).await?;
        return Err(io::Error::other("SOCKS authentication failed"));
    }
    io.write_all(&[1, 0]).await?;
    let mut request = [0; 4];
    io.read_exact(&mut request).await?;
    if request[0] != 5 || request[2] != 0 {
        return Err(io::Error::other("invalid SOCKS request"));
    }
    if !matches!(request[1], 1 | 3) {
        socks_reply(io, 7).await?;
        return Err(io::Error::other("SOCKS command unsupported"));
    }
    let address = match request[3] {
        1 => {
            let mut ip = [0; 4];
            io.read_exact(&mut ip).await?;
            Address::Ip(std::net::Ipv4Addr::from(ip).into())
        }
        4 => {
            let mut ip = [0; 16];
            io.read_exact(&mut ip).await?;
            Address::Ip(std::net::Ipv6Addr::from(ip).into())
        }
        3 => {
            let mut domain = vec![0; io.read_u8().await? as usize];
            io.read_exact(&mut domain).await?;
            if domain.is_empty() || domain.iter().any(|c| *c <= 32 || *c >= 127) {
                socks_reply(io, 8).await?;
                return Err(io::Error::other("invalid SOCKS domain"));
            }
            Address::Domain(domain)
        }
        _ => {
            socks_reply(io, 8).await?;
            return Err(io::Error::other("SOCKS address type"));
        }
    };
    let port = io.read_u16().await?;
    if port == 0 && request[1] == 1 {
        socks_reply(io, 8).await?;
        return Err(io::Error::other("zero target port"));
    }
    let target = Target { address, port };
    Ok(if request[1] == 3 { Request::Associate(target) } else { Request::Connect(target) })
}

fn constant_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let mut difference = 0;
    for (a, b) in a.iter().zip(b) {
        difference |= a ^ b;
    }
    difference == 0
}
pub(crate) async fn socks_reply(io: &mut (impl AsyncWrite + Unpin), code: u8) -> io::Result<()> {
    io.write_all(&[5, code, 0, 1, 0, 0, 0, 0, 0, 0]).await?;
    io.flush().await
}

pub(crate) struct VlessIo {
    inner: BoxIo,
    header: [u8; 2],
    header_read: usize,
    skip: usize,
}
impl VlessIo {
    pub fn new(inner: BoxIo) -> Self {
        Self {
            inner,
            header: [0; 2],
            header_read: 0,
            skip: 0,
        }
    }
}
impl AsyncRead for VlessIo {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let me = self.get_mut();
        if buf.remaining() == 0 {
            return Poll::Ready(Ok(()));
        }
        while me.header_read < 2 {
            let mut read = ReadBuf::new(&mut me.header[me.header_read..]);
            std::task::ready!(Pin::new(&mut me.inner).poll_read(cx, &mut read))?;
            let n = read.filled().len();
            if n == 0 {
                return Poll::Ready(Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "truncated VLESS response",
                )));
            }
            me.header_read += n;
            if me.header_read == 2 {
                me.skip = me.header[1] as usize;
            }
        }
        if me.header[0] != 0 {
            return Poll::Ready(Err(io::Error::other("invalid VLESS response version")));
        }
        while me.skip > 0 {
            let mut discard = [0; 255];
            let mut read = ReadBuf::new(&mut discard[..me.skip]);
            std::task::ready!(Pin::new(&mut me.inner).poll_read(cx, &mut read))?;
            let n = read.filled().len();
            if n == 0 {
                return Poll::Ready(Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "truncated VLESS addons",
                )));
            }
            me.skip -= n;
        }
        Pin::new(&mut me.inner).poll_read(cx, buf)
    }
}
impl AsyncWrite for VlessIo {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        data: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.get_mut().inner).poll_write(cx, data)
    }
    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().inner).poll_flush(cx)
    }
    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.get_mut().inner).poll_shutdown(cx)
    }
}
