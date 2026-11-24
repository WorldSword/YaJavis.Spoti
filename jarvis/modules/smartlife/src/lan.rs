use crate::crypto;
use jarvis_home_common::{now, random};
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, HashMap},
    net::{IpAddr, SocketAddr},
    sync::{
        Mutex,
        atomic::{AtomicU32, Ordering},
    },
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpStream, UdpSocket},
};
use zeroize::Zeroizing;

const PREFIX55: u32 = 0x55aa;
const SUFFIX55: u32 = 0xaa55;
const PREFIX66: u32 = 0x6699;
const SUFFIX66: u32 = 0x9966;
const TTL: Duration = Duration::from_secs(600);

#[derive(Clone, Copy)]
enum Version {
    V33,
    V34,
    V35,
}

#[derive(Clone)]
struct Peer {
    ip: IpAddr,
    version: Version,
    seen: Instant,
}

#[derive(Default)]
pub struct Lan {
    peers: Mutex<HashMap<String, Peer>>,
    seq: AtomicU32,
}

impl Lan {
    pub fn known(&self, id: &str) -> bool {
        self.peer(id).is_some()
    }
    fn peer(&self, id: &str) -> Option<Peer> {
        self.peers
            .lock()
            .unwrap()
            .get(id)
            .filter(|p| p.seen.elapsed() < TTL)
            .cloned()
    }

    pub async fn listen(&self, port: u16) {
        loop {
            if let Ok(socket) = UdpSocket::bind(("0.0.0.0", port)).await {
                let mut buffer = [0; 4096];
                while let Ok((n, from)) = socket.recv_from(&mut buffer).await {
                    self.announce(&buffer[..n], from);
                }
            }
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
    }

    fn announce(&self, packet: &[u8], from: SocketAddr) {
        let IpAddr::V4(ip) = from.ip() else {
            return;
        };
        if !ip.is_private() && !ip.is_link_local() {
            return;
        }
        let Ok(raw) = decode_udp(packet) else {
            return;
        };
        let Ok(info) = serde_json::from_slice::<Value>(&raw) else {
            return;
        };
        let Some(id) = info["gwId"]
            .as_str()
            .filter(|id| !id.is_empty() && id.len() <= 128)
        else {
            return;
        };
        let version = match info["version"].as_str().unwrap_or("") {
            "" | "3.1" | "3.2" | "3.3" => Version::V33,
            "3.4" => Version::V34,
            "3.5" => Version::V35,
            _ => return,
        };
        // Use the sender address, never an arbitrary target from the announcement.
        let mut peers = self.peers.lock().unwrap();
        if peers.len() >= 512 {
            peers.retain(|_, p| p.seen.elapsed() < TTL);
            if peers.len() >= 512 && !peers.contains_key(id) {
                return;
            }
        }
        peers.insert(
            id.to_owned(),
            Peer {
                ip: ip.into(),
                version,
                seen: Instant::now(),
            },
        );
    }

    pub async fn control(
        &self,
        id: &str,
        key: &str,
        dps: &BTreeMap<String, Value>,
    ) -> Result<(), String> {
        let peer = self.peer(id).ok_or("устройство не видно в домашней сети")?;
        let key: &[u8; 16] = key
            .as_bytes()
            .try_into()
            .map_err(|_| "нет локального ключа")?;
        let seq = self.seq.fetch_add(16, Ordering::Relaxed);
        tokio::time::timeout(Duration::from_millis(1500), async {
            let mut stream = TcpStream::connect((peer.ip, 6668))
                .await
                .map_err(|_| "устройство недоступно")?;
            stream
                .set_nodelay(true)
                .map_err(|_| "не настроить соединение Tuya")?;
            control(&mut stream, peer.version, id, key, seq, dps).await
        })
        .await
        .map_err(|_| "тайм-аут устройства")?
    }
}

fn word(bytes: &[u8], at: usize) -> Result<u32, String> {
    Ok(u32::from_be_bytes(
        bytes
            .get(at..at + 4)
            .ok_or("Короткий пакет Tuya")?
            .try_into()
            .unwrap(),
    ))
}

fn pack55(seq: u32, cmd: u32, payload: &[u8], key: Option<&[u8]>) -> Vec<u8> {
    let trailer = if key.is_some() { 36 } else { 8 };
    let mut packet = Vec::with_capacity(16 + payload.len() + trailer);
    for word in [PREFIX55, seq, cmd, (payload.len() + trailer) as u32] {
        packet.extend(word.to_be_bytes());
    }
    packet.extend(payload);
    if let Some(key) = key {
        packet.extend(crypto::mac(key, &packet));
    } else {
        packet.extend(crc32fast::hash(&packet).to_be_bytes());
    }
    packet.extend(SUFFIX55.to_be_bytes());
    packet
}

fn unpack55<'a>(packet: &'a [u8], key: Option<&[u8]>) -> Result<&'a [u8], String> {
    let n = word(packet, 12)? as usize;
    let trailer = if key.is_some() { 36 } else { 8 };
    if n < trailer || n != packet.len() - 16 || word(packet, packet.len() - 4)? != SUFFIX55 {
        return Err("Неверный кадр Tuya".into());
    }
    let body_end = packet.len() - trailer;
    if let Some(key) = key {
        crypto::verify(
            key,
            &packet[..body_end],
            &packet[body_end..packet.len() - 4],
        )?;
    } else if crc32fast::hash(&packet[..body_end]) != word(packet, body_end)? {
        return Err("Неверная контрольная сумма Tuya".into());
    }
    Ok(&packet[16..body_end])
}

fn pack66(seq: u32, cmd: u32, body: &[u8], key: &[u8]) -> Result<Vec<u8>, String> {
    let mut packet = PREFIX66.to_be_bytes().to_vec();
    packet.extend([0, 0]);
    for word in [seq, cmd, (body.len() + 28) as u32] {
        packet.extend(word.to_be_bytes());
    }
    let nonce = random::<12>()?;
    let encrypted = crypto::seal(key, nonce, body, &packet[4..])?;
    packet.extend(nonce);
    packet.extend(encrypted);
    packet.extend(SUFFIX66.to_be_bytes());
    Ok(packet)
}

fn unpack66(packet: &[u8], key: &[u8]) -> Result<Vec<u8>, String> {
    let n = word(packet, 14)? as usize;
    if n < 28
        || packet.len().checked_sub(22) != Some(n)
        || word(packet, packet.len() - 4)? != SUFFIX66
    {
        return Err("Неверный кадр Tuya GCM".into());
    }
    crypto::open(
        key,
        packet[18..30].try_into().unwrap(),
        &packet[30..packet.len() - 4],
        &packet[4..18],
    )
}

fn decode_udp(packet: &[u8]) -> Result<Vec<u8>, String> {
    let key = crypto::md5(b"yGAdlopoPVldABfn");
    let mut raw = match word(packet, 0)? {
        PREFIX55 => {
            let body = unpack55(packet, None)?;
            if body.starts_with(b"{") {
                body.to_vec()
            } else {
                let payload = if body.starts_with(&[0; 4]) {
                    &body[4..]
                } else {
                    body
                };
                if payload.starts_with(b"{") {
                    payload.to_vec()
                } else {
                    crypto::ecb(&key, payload, false, true)?
                }
            }
        }
        PREFIX66 => unpack66(packet, &key)?,
        _ => return Err("Неизвестный формат Tuya".into()),
    };
    if raw.starts_with(&[0; 4]) {
        raw.drain(..4);
    }
    while raw.last() == Some(&0) {
        raw.pop();
    }
    Ok(raw)
}

async fn read(
    stream: &mut TcpStream,
    version: Version,
    key: &[u8],
) -> Result<(u32, Vec<u8>), String> {
    let mut packet = vec![
        0;
        if matches!(version, Version::V35) {
            18
        } else {
            16
        }
    ];
    stream
        .read_exact(&mut packet)
        .await
        .map_err(|_| "Обрыв ответа Tuya")?;
    let (prefix, offset, cmd_at, tail) = match version {
        Version::V35 => (PREFIX66, 14, 10, 4),
        _ => (PREFIX55, 12, 8, 0),
    };
    let n = word(&packet, offset)? as usize;
    if word(&packet, 0)? != prefix || n > 65_536 {
        return Err("Неверный пакет Tuya".into());
    }
    let start = packet.len();
    packet.resize(start + n + tail, 0);
    stream
        .read_exact(&mut packet[start..])
        .await
        .map_err(|_| "Обрыв ответа Tuya")?;
    let body = match version {
        Version::V33 => unpack55(&packet, None)?.to_vec(),
        Version::V34 => unpack55(&packet, Some(key))?.to_vec(),
        Version::V35 => unpack66(&packet, key)?,
    };
    let cmd = word(&packet, cmd_at)?;
    // 3.5 negotiation replies may omit the return code.
    let payload = if matches!(version, Version::V35)
        && (body.starts_with(b"{") || cmd == 4 && body.len() == 48)
    {
        body
    } else if word(&body, 0)? == 0 {
        body[4..].to_vec()
    } else {
        return Err("Устройство отклонило команду Tuya".into());
    };
    Ok((cmd, payload))
}

async fn send(
    stream: &mut TcpStream,
    version: Version,
    key: &[u8],
    seq: u32,
    cmd: u32,
    body: &[u8],
) -> Result<(), String> {
    let packet = match version {
        Version::V33 => pack55(seq, cmd, body, None),
        Version::V34 => pack55(seq, cmd, &crypto::ecb(key, body, true, true)?, Some(key)),
        Version::V35 => pack66(seq, cmd, body, key)?,
    };
    stream
        .write_all(&packet)
        .await
        .map_err(|_| "Не отправить команду Tuya".into())
}

async fn control(
    stream: &mut TcpStream,
    version: Version,
    id: &str,
    key: &[u8; 16],
    seq: u32,
    dps: &BTreeMap<String, Value>,
) -> Result<(), String> {
    if matches!(version, Version::V33) {
        let request = json!({"devId":id,"uid":id,"t":(now()/1000).to_string(),"dps":dps});
        let mut payload = b"3.3".to_vec();
        payload.resize(15, 0);
        payload.extend(crypto::ecb(
            key,
            request.to_string().as_bytes(),
            true,
            true,
        )?);
        send(stream, version, key, seq, 7, &payload).await?;
        if read(stream, version, key).await?.0 != 7 {
            return Err("Не получено подтверждение Tuya".into());
        }
        return Ok(());
    }
    let local = Zeroizing::new(random::<16>()?);
    send(stream, version, key, seq + 1, 3, &local[..]).await?;
    let (cmd, mut reply) = read(stream, version, key).await?;
    if cmd != 4 {
        return Err("Неверный ответ на обмен ключами Tuya".into());
    }
    if matches!(version, Version::V34) {
        reply = crypto::ecb(key, &reply, false, true)?;
    }
    if reply.len() != 48 {
        return Err("Неверный размер ключа Tuya".into());
    }
    crypto::verify(key, &local[..], &reply[16..])?;
    send(
        stream,
        version,
        key,
        seq + 2,
        5,
        &crypto::mac(key, &reply[..16]),
    )
    .await?;
    let xor = Zeroizing::new(std::array::from_fn::<_, 16, _>(|i| local[i] ^ reply[i]));
    let session = Zeroizing::new(match version {
        Version::V34 => crypto::ecb(key, &xor[..], true, false)?,
        Version::V35 => {
            crypto::seal(key, local[..12].try_into().unwrap(), &xor[..], &[])?[..16].to_vec()
        }
        Version::V33 => unreachable!(),
    });
    let request = json!({"protocol":5,"t":now()/1000,"data":{"dps":dps}});
    let mut payload = if matches!(version, Version::V35) {
        b"3.5".to_vec()
    } else {
        b"3.4".to_vec()
    };
    payload.resize(15, 0);
    payload.extend(request.to_string().as_bytes());
    send(stream, version, &session, seq + 3, 13, &payload).await?;
    if !matches!(read(stream, version, &session).await?.0, 7 | 13) {
        return Err("Не получено подтверждение Tuya".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::net::TcpListener;

    #[tokio::test]
    async fn local_control_requires_authenticated_confirmation() {
        let key = *b"0123456789abcdef";
        for version in [Version::V33, Version::V34, Version::V35] {
            for accept in [true, false] {
                tokio::time::timeout(Duration::from_secs(3), async {
                    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
                    let mut client = TcpStream::connect(listener.local_addr().unwrap())
                        .await
                        .unwrap();
                    let (mut peer, _) = listener.accept().await.unwrap();
                    let dps =
                        BTreeMap::from([("20".into(), json!(true)), ("22".into(), json!(500))]);
                    let fixture = async {
                        let mut session = key.to_vec();
                        let first = frame(&mut peer, version).await;
                        let command = if matches!(version, Version::V33) {
                            first
                        } else {
                            assert_eq!(
                                word(
                                    &first,
                                    if matches!(version, Version::V35) {
                                        10
                                    } else {
                                        8
                                    }
                                )
                                .unwrap(),
                                3
                            );
                            let local = client_body(&first, version, &key);
                            let remote = *b"test-peer-nonce!";
                            let mut reply = remote.to_vec();
                            reply.extend(crypto::mac(&key, &local));
                            send_response(&mut peer, version, &key, 4, 0, &reply).await;
                            let finish = frame(&mut peer, version).await;
                            assert_eq!(
                                client_body(&finish, version, &key),
                                crypto::mac(&key, &remote)
                            );
                            let xor: Vec<_> =
                                local.iter().zip(remote).map(|(a, b)| a ^ b).collect();
                            session = if matches!(version, Version::V35) {
                                crypto::seal(&key, local[..12].try_into().unwrap(), &xor, &[])
                                    .unwrap()[..16]
                                    .to_vec()
                            } else {
                                crypto::ecb(&key, &xor, true, false).unwrap()
                            };
                            frame(&mut peer, version).await
                        };
                        let body = client_body(&command, version, &session);
                        let payload = if matches!(version, Version::V33) {
                            crypto::ecb(&session, &body[15..], false, true).unwrap()
                        } else {
                            body[15..].to_vec()
                        };
                        let value: Value = serde_json::from_slice(&payload).unwrap();
                        let decoded = if matches!(version, Version::V33) {
                            &value["dps"]
                        } else {
                            &value["data"]["dps"]
                        };
                        assert_eq!(decoded, &json!(dps));
                        let cmd = if matches!(version, Version::V33) {
                            7
                        } else {
                            13
                        };
                        send_response(
                            &mut peer,
                            version,
                            &session,
                            cmd,
                            if accept { 0 } else { 1 },
                            &[],
                        )
                        .await;
                    };
                    let (result, ()) = tokio::join!(
                        control(&mut client, version, "dev1", &key, 16, &dps),
                        fixture
                    );
                    assert_eq!(result.is_ok(), accept);
                })
                .await
                .unwrap();
            }
        }
    }

    async fn frame(peer: &mut TcpStream, version: Version) -> Vec<u8> {
        let (head, length, tail) = if matches!(version, Version::V35) {
            (18, 14, 4)
        } else {
            (16, 12, 0)
        };
        let mut packet = vec![0; head];
        peer.read_exact(&mut packet).await.unwrap();
        packet.resize(head + word(&packet, length).unwrap() as usize + tail, 0);
        peer.read_exact(&mut packet[head..]).await.unwrap();
        packet
    }
    fn client_body(packet: &[u8], version: Version, key: &[u8]) -> Vec<u8> {
        match version {
            Version::V33 => unpack55(packet, None).unwrap().to_vec(),
            Version::V34 => {
                crypto::ecb(key, unpack55(packet, Some(key)).unwrap(), false, true).unwrap()
            }
            Version::V35 => unpack66(packet, key).unwrap(),
        }
    }
    async fn send_response(
        peer: &mut TcpStream,
        version: Version,
        key: &[u8],
        cmd: u32,
        code: u32,
        body: &[u8],
    ) {
        let mut reply = code.to_be_bytes().to_vec();
        reply.extend(if matches!(version, Version::V34) && !body.is_empty() {
            crypto::ecb(key, body, true, true).unwrap()
        } else {
            body.to_vec()
        });
        let packet = match version {
            Version::V33 => pack55(17, cmd, &reply, None),
            Version::V34 => pack55(17, cmd, &reply, Some(key)),
            Version::V35 => pack66(17, cmd, &reply, key).unwrap(),
        };
        for part in packet.chunks(7) {
            peer.write_all(part).await.unwrap();
        }
    }
}
