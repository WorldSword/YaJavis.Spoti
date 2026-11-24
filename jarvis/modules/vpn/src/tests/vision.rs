use super::*;
use tokio::io::{AsyncRead, AsyncWrite};

async fn request(io: &mut (impl AsyncRead + Unpin)) {
    let mut prefix = [0; 18];
    io.read_exact(&mut prefix).await.unwrap();
    assert_eq!(prefix[0], 0);
    let mut addon = vec![0; prefix[17] as usize];
    io.read_exact(&mut addon).await.unwrap();
    assert_eq!(&addon[2..], b"xtls-rprx-vision");
    let mut target = [0; 28];
    io.read_exact(&mut target).await.unwrap();
    assert_eq!(&target[..5], &[1, 1, 187, 2, 23]);
    assert_eq!(&target[5..], b"no-local-dns.invalid.xx");
}
async fn read_frame(io: &mut (impl AsyncRead + Unpin), first: bool) -> (u8, Vec<u8>) {
    if first {
        let mut uuid = [0; 16];
        io.read_exact(&mut uuid).await.unwrap();
        assert_eq!(hex::encode(uuid), UUID.replace('-', ""));
    }
    let mut header = [0; 5];
    io.read_exact(&mut header).await.unwrap();
    let n = u16::from_be_bytes([header[1], header[2]]) as usize;
    let padding = u16::from_be_bytes([header[3], header[4]]) as usize;
    let mut payload = vec![0; n];
    io.read_exact(&mut payload).await.unwrap();
    let mut pad = vec![0; padding];
    io.read_exact(&mut pad).await.unwrap();
    (header[0], payload)
}
async fn frame(io: &mut (impl AsyncWrite + Unpin), payload: &[u8], first: bool, command: u8) {
    let mut bytes = Vec::new();
    if first {
        bytes.extend(hex::decode(UUID.replace('-', "")).unwrap());
    }
    bytes.push(command);
    bytes.extend((payload.len() as u16).to_be_bytes());
    bytes.extend(17u16.to_be_bytes());
    bytes.extend(payload);
    bytes.extend([0; 17]);
    // Deliberately split the UUID, header, content and padding across separate TLS records.
    for part in bytes.chunks(3) {
        io.write_all(part).await.unwrap();
        io.flush().await.unwrap();
    }
}
fn server_hello() -> Vec<u8> {
    let mut body = vec![3, 3];
    body.extend([0; 32]);
    body.push(0);
    body.extend([0x13, 1, 0, 0, 6, 0, 43, 0, 2, 3, 4]);
    let mut hello = vec![
        22,
        3,
        3,
        0,
        (body.len() + 4) as u8,
        2,
        0,
        0,
        body.len() as u8,
    ];
    hello.extend(body);
    hello
}

#[tokio::test]
async fn tls_vision_handles_fragmented_prefix_and_both_directional_raw_switches() {
    tokio::time::timeout(Duration::from_secs(5),async{
        let listener=TcpListener::bind("127.0.0.1:0").await.unwrap();let port=listener.local_addr().unwrap().port();
        let hello=server_hello();let expected=hello.clone();
        let server=tokio::spawn(async move{
            let (socket,_)=listener.accept().await.unwrap();let mut io=test_tls().accept(socket).await.unwrap();request(&mut io).await;
            let (command,payload)=read_frame(&mut io,true).await;assert_eq!(command,0);assert_eq!(payload,&[22,3,1,0,1,1]);
            io.write_all(&[0,0]).await.unwrap();frame(&mut io,&hello,true,2).await;
            // Once DIRECT padding is consumed, subsequent bytes are raw TCP, not outer TLS.
            io.get_mut().0.write_all(b"raw-server-tail").await.unwrap();
            let (command,payload)=read_frame(&mut io,false).await;assert_eq!(command,2);assert_eq!(payload,&[23,3,3,0,4,1,2,3,4]);
            io.get_mut().0.write_all(b"ack").await.unwrap();
            let mut raw=[0;15];io.get_mut().0.read_exact(&mut raw).await.unwrap();assert_eq!(&raw,b"raw-client-tail");
        });
        let vpn=Vpn::new();let state=vpn.request(json!({"link":format!("vless://{UUID}@127.0.0.1:{port}?security=tls&sni=vpn.example.org&allowInsecure=1&flow=xtls-rprx-vision")})).await.unwrap();
        let mut socket=socks(&state,"no-local-dns.invalid.xx",443).await;socket.write_all(&[22,3,1,0,1,1]).await.unwrap();
        let mut response=vec![0;expected.len()+15];socket.read_exact(&mut response).await.unwrap();assert_eq!(&response[..expected.len()],&expected);assert_eq!(&response[expected.len()..],b"raw-server-tail");
        socket.write_all(&[23,3,3,0,4,1,2,3,4]).await.unwrap();let mut ack=[0;3];socket.read_exact(&mut ack).await.unwrap();assert_eq!(&ack,b"ack");
        socket.write_all(b"raw-client-tail").await.unwrap();server.await.unwrap();vpn.stop().await;
    }).await.unwrap();
}

#[tokio::test]
async fn tls_vision_rejects_bad_uuid_commands_and_truncated_padding() {
    tokio::time::timeout(Duration::from_secs(8),async{
        for bad in [0,1,2]{
            let listener=TcpListener::bind("127.0.0.1:0").await.unwrap();let port=listener.local_addr().unwrap().port();
            let server=tokio::spawn(async move{
                let (socket,_)=listener.accept().await.unwrap();let mut io=test_tls().accept(socket).await.unwrap();request(&mut io).await;read_frame(&mut io,true).await;
                io.write_all(&[0,0]).await.unwrap();let uuid=if bad==0 {vec![9;16]}else{hex::decode(UUID.replace('-',"")).unwrap()};io.write_all(&uuid).await.unwrap();
                if bad!=0 {io.write_all(&[if bad==1 {3}else{2},0,0,0,10]).await.unwrap();}
                io.flush().await.unwrap();io.shutdown().await.unwrap();
            });
            let vpn=Vpn::new();let state=vpn.request(json!({"link":format!("vless://{UUID}@127.0.0.1:{port}?security=tls&sni=vpn.example.org&allowInsecure=1&flow=xtls-rprx-vision")})).await.unwrap();
            let mut socket=socks(&state,"no-local-dns.invalid.xx",443).await;socket.write_all(b"ping").await.unwrap();assert!(socket.read_u8().await.is_err());server.await.unwrap();vpn.stop().await;
        }
    }).await.unwrap();
}
