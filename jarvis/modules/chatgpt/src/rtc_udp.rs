//! Poll-based WebRTC datagrams over an authenticated, loopback-only SOCKS UDP association.
//! The socket supplied by ICE is retained for its candidate address but never sends media.
use super::rtc_runtime::Route;
use std::{
    future::Future,
    io::{self, IoSliceMut},
    net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr, UdpSocket},
    pin::Pin,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    task::{Context, Poll, Waker},
    time::Duration,
};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWriteExt, ReadBuf},
    net::{TcpStream, UdpSocket as AsyncSocket},
};
use webrtc::runtime::{AsyncUdpSocket, RecvMeta, Transmit};

type Opening = Pin<Box<dyn Future<Output = io::Result<Association>> + Send>>;
enum State {
    Opening(Opening),
    Ready(Association),
    Failed(io::ErrorKind),
}
struct Association {
    control: TcpStream,
    relay: AsyncSocket,
}
pub(super) struct Socket {
    candidate: UdpSocket,
    armed: Arc<AtomicBool>,
    arm_wakers: Arc<Mutex<Vec<Waker>>>,
    state: Mutex<State>,
}
impl std::fmt::Debug for Socket {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("SocksUdpSocket")
    }
}
impl Socket {
    pub fn new(
        candidate: UdpSocket,
        route: Route,
        armed: Arc<AtomicBool>,
        arm_wakers: Arc<Mutex<Vec<Waker>>>,
    ) -> io::Result<Self> {
        candidate.local_addr()?;
        Ok(Self {
            candidate,
            armed,
            arm_wakers,
            state: Mutex::new(State::Opening(Box::pin(async move {
                tokio::time::timeout(Duration::from_secs(10), open(route))
                    .await
                    .map_err(|_| {
                        io::Error::new(io::ErrorKind::TimedOut, "SOCKS UDP association timeout")
                    })?
            }))),
        })
    }

    fn arm_now(&self) {
        let mut wakers = self.arm_wakers.lock().unwrap();
        self.armed.store(true, Ordering::Release);
        for waker in wakers.drain(..) {
            waker.wake();
        }
    }
}
fn ready<'a>(state: &'a mut State, cx: &mut Context<'_>) -> Poll<io::Result<&'a mut Association>> {
    if let State::Opening(future) = state {
        *state = match std::task::ready!(future.as_mut().poll(cx)) {
            Ok(association) => State::Ready(association),
            Err(e) => State::Failed(e.kind()),
        };
    }
    if let State::Ready(association) = state {
        // Register the receiver's waker on the control connection too. Losing the VPN
        // terminates ICE immediately instead of leaving media waiting on a dead UDP port.
        let mut byte = [0];
        if let Poll::Ready(result) =
            Pin::new(&mut association.control).poll_read(cx, &mut ReadBuf::new(&mut byte))
        {
            let kind = result
                .err()
                .map(|e| e.kind())
                .unwrap_or(io::ErrorKind::ConnectionAborted);
            *state = State::Failed(kind);
        }
    }
    match state {
        State::Ready(a) => Poll::Ready(Ok(a)),
        State::Failed(kind) => {
            Poll::Ready(Err(io::Error::new(*kind, "SOCKS UDP association closed")))
        }
        State::Opening(_) => unreachable!(),
    }
}
async fn open(route: Route) -> io::Result<Association> {
    let local: SocketAddr = if route.address.is_ipv4() {
        (Ipv4Addr::LOCALHOST, 0).into()
    } else {
        (Ipv6Addr::LOCALHOST, 0).into()
    };
    let relay = AsyncSocket::bind(local).await?;
    let mut control = TcpStream::connect(route.address).await?;
    control.set_nodelay(true)?;
    control.write_all(&[5, 1, 2]).await?;
    let mut reply = [0; 2];
    control.read_exact(&mut reply).await?;
    if reply != [5, 2] {
        return Err(io::Error::other("SOCKS authentication required"));
    }
    let mut auth = vec![1, route.user.len() as u8];
    auth.extend(route.user);
    auth.push(route.pass.len() as u8);
    auth.extend(route.pass);
    control.write_all(&auth).await?;
    control.read_exact(&mut reply).await?;
    if reply != [1, 0] {
        return Err(io::Error::other("SOCKS authentication rejected"));
    }
    let mut request = encode(relay.local_addr()?, &[])?;
    request[..3].copy_from_slice(&[5, 3, 0]);
    control.write_all(&request).await?;
    let mut header = [0; 4];
    control.read_exact(&mut header).await?;
    if header[..3] != [5, 0, 0] {
        return Err(io::Error::other("VPN rejected UDP association"));
    }
    let address: IpAddr = match header[3] {
        1 => {
            let mut ip = [0; 4];
            control.read_exact(&mut ip).await?;
            Ipv4Addr::from(ip).into()
        }
        4 => {
            let mut ip = [0; 16];
            control.read_exact(&mut ip).await?;
            Ipv6Addr::from(ip).into()
        }
        _ => return Err(io::Error::other("VPN UDP relay must have a loopback IP")),
    };
    let port = control.read_u16().await?;
    if !address.is_loopback() || port == 0 {
        return Err(io::Error::other("VPN UDP relay must be local"));
    }
    relay.connect((address, port)).await?;
    Ok(Association { control, relay })
}
fn encode(target: SocketAddr, payload: &[u8]) -> io::Result<Vec<u8>> {
    if payload.len() > 8_190 || target.port() == 0 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "ICE UDP packet too large or invalid destination",
        ));
    }
    let mut bytes = vec![0, 0, 0];
    match target.ip() {
        IpAddr::V4(ip) => {
            bytes.push(1);
            bytes.extend(ip.octets());
        }
        IpAddr::V6(ip) => {
            bytes.push(4);
            bytes.extend(ip.octets());
        }
    }
    bytes.extend(target.port().to_be_bytes());
    bytes.extend(payload);
    Ok(bytes)
}
fn decode(bytes: &[u8]) -> Option<(SocketAddr, &[u8])> {
    if bytes.get(..3)? != [0, 0, 0] {
        return None;
    }
    let (ip, end): (IpAddr, usize) = match *bytes.get(3)? {
        1 => (
            Ipv4Addr::from(<[u8; 4]>::try_from(bytes.get(4..8)?).ok()?).into(),
            8,
        ),
        4 => (
            Ipv6Addr::from(<[u8; 16]>::try_from(bytes.get(4..20)?).ok()?).into(),
            20,
        ),
        _ => return None,
    };
    let port = u16::from_be_bytes(bytes.get(end..end + 2)?.try_into().ok()?);
    if port == 0 {
        return None;
    }
    Some((SocketAddr::new(ip, port), bytes.get(end + 2..)?))
}
impl AsyncUdpSocket for Socket {
    fn local_addr(&self) -> io::Result<SocketAddr> {
        self.candidate.local_addr()
    }
    fn poll_send(&self, cx: &mut Context<'_>, transmit: &Transmit<'_>) -> Poll<io::Result<usize>> {
        if !self.armed.load(Ordering::Acquire) {
            self.arm_now();
        }
        let packet = encode(transmit.destination, transmit.contents)?;
        let mut state = self.state.lock().unwrap();
        let association = std::task::ready!(ready(&mut state, cx))?;
        std::task::ready!(association.relay.poll_send(cx, &packet))?;
        Poll::Ready(Ok(transmit.contents.len()))
    }
    fn poll_recv(
        &self,
        cx: &mut Context<'_>,
        bufs: &mut [IoSliceMut<'_>],
        meta: &mut [RecvMeta],
    ) -> Poll<io::Result<usize>> {
        if bufs.is_empty() || meta.is_empty() {
            return Poll::Ready(Ok(0));
        }
        if !self.armed.load(Ordering::Acquire) {
            let mut wakers = self.arm_wakers.lock().unwrap();
            if !self.armed.load(Ordering::Acquire) {
                if !wakers.iter().any(|w| w.will_wake(cx.waker())) {
                    wakers.push(cx.waker().clone());
                }
                return Poll::Pending;
            }
        }
        let mut state = self.state.lock().unwrap();
        let association = std::task::ready!(ready(&mut state, cx))?;
        let mut packet = [0; 65_535];
        // Limit malformed relay packets per poll, so a noisy relay cannot starve the reactor.
        for _ in 0..8 {
            let mut buffer = ReadBuf::new(&mut packet);
            std::task::ready!(association.relay.poll_recv(cx, &mut buffer))?;
            let Some((addr, payload)) = decode(buffer.filled()) else {
                continue;
            };
            if payload.len() > bufs[0].len() {
                continue;
            } // never pass a truncated RTP/DTLS packet
            bufs[0][..payload.len()].copy_from_slice(payload);
            meta[0] = RecvMeta::default();
            meta[0].addr = addr;
            meta[0].len = payload.len();
            meta[0].stride = payload.len().max(1);
            return Poll::Ready(Ok(1));
        }
        cx.waker().wake_by_ref();
        Poll::Pending
    }
}
