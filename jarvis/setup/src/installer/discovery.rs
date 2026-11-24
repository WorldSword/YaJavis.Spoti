use std::{collections::BTreeSet, net::Ipv4Addr, time::Duration};

use super::DEFAULT_ADB_PORT;

const PARALLEL: usize = 64;

fn subnet(ip: Ipv4Addr, mask: Ipv4Addr) -> Option<(u32, u32)> {
    if !ip.is_private() {
        return None;
    }
    let ip = u32::from(ip);
    let mask = u32::from(mask);
    if !(8..=30).contains(&mask.leading_ones()) || mask.count_ones() != mask.leading_ones() {
        return None;
    }
    // Bound a search even on a large network. A /25 or smaller keeps its real boundaries.
    let mask = mask | 0xffff_ff00;
    Some((ip & mask, (ip & mask) | !mask))
}

pub async fn nearby(already: &BTreeSet<Ipv4Addr>) -> Vec<Ipv4Addr> {
    let Ok(interfaces) = if_addrs::get_if_addrs() else {
        return Vec::new();
    };
    let addresses: Vec<_> = interfaces
        .iter()
        .filter_map(|interface| match &interface.addr {
            if_addrs::IfAddr::V4(address) => Some(address),
            _ => None,
        })
        .collect();
    let own: BTreeSet<_> = addresses.iter().map(|address| address.ip).collect();
    let ranges: BTreeSet<_> = addresses
        .iter()
        .filter_map(|address| subnet(address.ip, address.netmask))
        .take(8)
        .collect();
    let mut hosts = ranges
        .into_iter()
        .take(4)
        .flat_map(|(start, end)| (start + 1..end).map(Ipv4Addr::from))
        .filter(|ip| !own.contains(ip) && !already.contains(ip));
    let mut requests = tokio::task::JoinSet::new();
    let mut open = Vec::new();
    loop {
        while requests.len() < PARALLEL {
            let Some(ip) = hosts.next() else { break };
            requests.spawn(async move {
                let connected = tokio::time::timeout(
                    Duration::from_millis(350),
                    tokio::net::TcpStream::connect((ip, DEFAULT_ADB_PORT)),
                )
                .await
                .is_ok_and(|result| result.is_ok());
                connected.then_some(ip)
            });
        }
        match requests.join_next().await {
            Some(Ok(Some(ip))) => open.push(ip),
            Some(_) => {}
            None => break,
        }
    }
    open.sort_unstable();
    open
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn discovery_stays_in_private_connected_subnets() {
        let range = |ip: &str, mask: &str| {
            subnet(ip.parse().unwrap(), mask.parse().unwrap())
                .map(|(a, b)| (Ipv4Addr::from(a), Ipv4Addr::from(b)))
        };
        assert_eq!(
            range("192.168.2.72", "255.255.0.0"),
            Some(([192, 168, 2, 0].into(), [192, 168, 2, 255].into()))
        );
        assert_eq!(
            range("192.168.2.172", "255.255.255.128"),
            Some(([192, 168, 2, 128].into(), [192, 168, 2, 255].into()))
        );
        assert!(range("8.8.8.8", "255.255.255.0").is_none());
        assert!(range("10.0.0.1", "255.0.255.0").is_none());
    }
}
