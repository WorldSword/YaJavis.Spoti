use super::*;
use tokio::net::UdpSocket;

pub(super) async fn association(state: &Value) -> (TcpStream, UdpSocket, std::net::SocketAddr) {
    let udp = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let mut control = TcpStream::connect(state["socks"].as_str().unwrap())
        .await
        .unwrap();
    control.write_all(&[5, 1, 2]).await.unwrap();
    assert_eq!(control.read_u16().await.unwrap(), 0x0502);
    let (user, pass) = (
        state["user"].as_str().unwrap().as_bytes(),
        state["pass"].as_str().unwrap().as_bytes(),
    );
    let mut auth = vec![1, user.len() as u8];
    auth.extend(user);
    auth.push(pass.len() as u8);
    auth.extend(pass);
    control.write_all(&auth).await.unwrap();
    assert_eq!(control.read_u16().await.unwrap(), 0x0100);
    let mut request = vec![5, 3, 0, 1, 127, 0, 0, 1];
    request.extend(udp.local_addr().unwrap().port().to_be_bytes());
    control.write_all(&request).await.unwrap();
    let mut reply = [0; 10];
    control.read_exact(&mut reply).await.unwrap();
    assert_eq!(&reply[..8], &[5, 0, 0, 1, 127, 0, 0, 1]);
    (
        control,
        udp,
        ([127, 0, 0, 1], u16::from_be_bytes([reply[8], reply[9]])).into(),
    )
}
pub(super) fn packet(target: &str, port: u16, payload: &[u8]) -> Vec<u8> {
    let mut out = vec![0, 0, 0, 3, target.len() as u8];
    out.extend(target.as_bytes());
    out.extend(port.to_be_bytes());
    out.extend(payload);
    out
}

#[tokio::test]
async fn udp_preserves_boundaries_remote_dns_and_control_lifetime() {
    tokio::time::timeout(Duration::from_secs(5), async {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let vpn = Vpn::new();
        let state = vpn
            .request(json!({"link":format!("vless://{UUID}@{}",listener.local_addr().unwrap())}))
            .await
            .unwrap();
        assert_eq!(state["udp"], true);
        let server = tokio::spawn(async move {
            let (mut io, _) = listener.accept().await.unwrap();
            let mut prefix = [0; 23];
            io.read_exact(&mut prefix).await.unwrap();
            assert_eq!(&prefix[17..], &[0, 2, 0, 53, 2, 18]);
            let mut domain = [0; 18];
            io.read_exact(&mut domain).await.unwrap();
            assert_eq!(&domain, b"remote-dns.invalid");
            io.write_all(&[0, 2, 9, 8]).await.unwrap();
            for expected in [b"first".as_slice(), b"".as_slice(), b"third".as_slice()] {
                let len = io.read_u16().await.unwrap() as usize;
                let mut data = vec![0; len];
                io.read_exact(&mut data).await.unwrap();
                assert_eq!(data, expected);
                // Deliberately fragment both framing and payload.
                let mut frame = (len as u16).to_be_bytes().to_vec();
                frame.extend(&data);
                for byte in frame {
                    io.write_all(&[byte]).await.unwrap();
                    tokio::task::yield_now().await;
                }
            }
            assert!(io.read_u8().await.is_err());
        });
        let (control, udp, relay) = association(&state).await;
        let other = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        other
            .send_to(&packet("remote-dns.invalid", 53, b"wrong-port"), relay)
            .await
            .unwrap();
        let mut fragmented = packet("remote-dns.invalid", 53, b"fragment");
        fragmented[2] = 1;
        udp.send_to(&fragmented, relay).await.unwrap();
        for payload in [b"first".as_slice(), b"".as_slice(), b"third".as_slice()] {
            let sent = packet("remote-dns.invalid", 53, payload);
            udp.send_to(&sent, relay).await.unwrap();
            let mut response = [0; 1024];
            let (n, from) = udp.recv_from(&mut response).await.unwrap();
            assert_eq!(from, relay);
            assert_eq!(&response[..n], &sent);
        }
        drop(control);
        server.await.unwrap();
        udp.send_to(&packet("remote-dns.invalid", 53, b"closed"), relay)
            .await
            .unwrap();
        assert!(!matches!(
            tokio::time::timeout(Duration::from_millis(75), udp.recv(&mut [0; 32])).await,
            Ok(Ok(_))
        ));
        vpn.stop().await;
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn udp_replacement_closes_association_and_vision_advertises_xudp() {
    tokio::time::timeout(Duration::from_secs(4),async{
        let vpn=Vpn::new();let state=vpn.request(json!({"link":format!("vless://{UUID}@127.0.0.1:9")})).await.unwrap();
        let (mut control,_,_)=association(&state).await;
        vpn.request(json!({"link":""})).await.unwrap();assert!(control.read_u8().await.is_err());
        let state=vpn.request(json!({"link":format!("vless://{UUID}@127.0.0.1:9?security=tls&flow=xtls-rprx-vision")})).await.unwrap();
        assert_eq!(state["udp"],true);vpn.stop().await;
    }).await.unwrap();
}

#[test]
fn malformed_udp_packets_never_panic_or_resolve_locally() {
    let full = packet("remote.invalid", 443, b"test");
    for end in 0..full.len() - 4 {
        assert!(crate::udp::decode(&full[..end]).is_none());
    }
    for index in [0, 1, 2, 3] {
        let mut invalid = full.clone();
        invalid[index] = 255;
        assert!(crate::udp::decode(&invalid).is_none());
    }
    let (target, payload) = crate::udp::decode(&full).unwrap();
    assert_eq!(target.port, 443);
    assert_eq!(payload, b"test");
}

#[tokio::test]
async fn vision_xudp_predials_and_reuses_single_tunnel_or_aborts_on_close() {
    tokio::time::timeout(Duration::from_secs(6), async {
        let acceptor = crate::tests::test_tls();
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let accepted_count = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let count = accepted_count.clone();
        let server = tokio::spawn(async move {
            // 1. Unused association: pre-dial completes Mux header, then closes cleanly on drop(control).
            let (socket, _) = listener.accept().await.unwrap();
            count.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            let mut io = acceptor.accept(socket).await.unwrap();
            let mut head = [0; 18];
            io.read_exact(&mut head).await.unwrap();
            assert_eq!(head[0], 0);
            let mut addon = vec![0; head[17] as usize];
            io.read_exact(&mut addon).await.unwrap();
            assert_eq!(io.read_u8().await.unwrap(), 3); // Mux (XUDP)
            let mut eof = [0; 64];
            assert!(matches!(io.read(&mut eof).await, Ok(0) | Err(_)));

            // 2. Used association: same pre-dialed tunnel carries first XUDP datagram + reply.
            let (socket, _) = listener.accept().await.unwrap();
            count.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            let mut io = acceptor.accept(socket).await.unwrap();
            let mut head = [0; 18];
            io.read_exact(&mut head).await.unwrap();
            let mut addon = vec![0; head[17] as usize];
            io.read_exact(&mut addon).await.unwrap();
            assert_eq!(io.read_u8().await.unwrap(), 3);
            let mut vhead = [0; 21];
            io.read_exact(&mut vhead).await.unwrap();
            assert_eq!(&vhead[..16], &head[1..17]);
            let content_len = u16::from_be_bytes([vhead[17], vhead[18]]) as usize;
            let pad_len = u16::from_be_bytes([vhead[19], vhead[20]]) as usize;
            let mut frame = vec![0; content_len + pad_len];
            io.read_exact(&mut frame).await.unwrap();
            let meta_len = u16::from_be_bytes([frame[0], frame[1]]) as usize;
            let payload_len =
                u16::from_be_bytes([frame[2 + meta_len], frame[2 + meta_len + 1]]) as usize;
            assert_eq!(
                &frame[4 + meta_len..4 + meta_len + payload_len],
                b"hello"
            );

            io.write_all(&[0, 0]).await.unwrap();
            let xudp_reply = [
                0, 4, 0, 0, 2 /* XUDP_KEEP */, 1 /* XUDP_DATA */, 0, 5, b'w', b'o', b'r',
                b'l', b'd',
            ];
            let mut reply_frame = Vec::with_capacity(16 + 5 + xudp_reply.len());
            reply_frame.extend_from_slice(&head[1..17]);
            reply_frame.push(1);
            reply_frame.extend((xudp_reply.len() as u16).to_be_bytes());
            reply_frame.extend(0u16.to_be_bytes());
            reply_frame.extend_from_slice(&xudp_reply);
            io.write_all(&reply_frame).await.unwrap();
            io.flush().await.unwrap();
            let mut tail = [0; 32];
            let _ = io.read(&mut tail).await;
        });

        let vpn = Vpn::new();
        let state = vpn
            .request(json!({"link": format!("vless://{UUID}@127.0.0.1:{port}?security=tls&sni=vpn.example.org&allowInsecure=1&flow=xtls-rprx-vision")}))
            .await
            .unwrap();
        let (control, _udp, _relay) = association(&state).await;
        for _ in 0..100 {
            if accepted_count.load(std::sync::atomic::Ordering::SeqCst) >= 1 {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        assert_eq!(accepted_count.load(std::sync::atomic::Ordering::SeqCst), 1);
        drop(control);
        tokio::time::sleep(Duration::from_millis(30)).await;

        let (control, udp, relay) = association(&state).await;
        udp.send_to(&packet("1.1.1.1", 53, b"hello"), relay)
            .await
            .unwrap();
        let mut response = [0; 256];
        let (n, from) = udp.recv_from(&mut response).await.unwrap();
        assert_eq!(from, relay);
        assert_eq!(
            crate::udp::decode(&response[..n]).unwrap().1,
            b"world"
        );
        assert_eq!(accepted_count.load(std::sync::atomic::Ordering::SeqCst), 2);
        drop(control);
        server.await.unwrap();
        vpn.stop().await;
    })
    .await
    .unwrap();
}
