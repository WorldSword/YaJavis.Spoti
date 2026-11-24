pub mod backup;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    path::Path,
};

pub const CORE: &str = "jarvisd";
pub const NEXT: &str = "jarvisd.next-rust";
pub const PREVIOUS: &str = "jarvisd.prev-rust";
pub const PENDING: &str = "update-rust.json";
pub const FAILURE: &str = "update-rust.error";
pub const MAX_BINARY: u64 = 32 << 20;
// Стабильный идентификатор формата для OTA-обновлений с 0.10.x — менять нельзя.
pub const IDENTITY: &str = "yajavis-rust-1";

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct Image {
    pub format: String,
    pub version: String,
    pub arch: String,
    pub modules: Vec<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct Pending {
    pub version: String,
    pub sha256: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub module: Option<String>,
}

pub fn arch() -> &'static str {
    if cfg!(target_arch = "aarch64") {
        "arm64"
    } else if cfg!(target_arch = "arm") {
        "armv7"
    } else {
        std::env::consts::ARCH
    }
}

pub fn sha256(path: &Path) -> io::Result<String> {
    let mut input = File::open(path)?;
    let mut hash = Sha256::new();
    let mut buf = [0; 64 * 1024];
    loop {
        let n = input.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hash.update(&buf[..n]);
    }
    Ok(format!("{:x}", hash.finalize()))
}

pub fn checksum(bytes: &str, asset: &str) -> Option<String> {
    let mut matches = bytes.lines().filter_map(|line| {
        let mut pieces = line.split_whitespace();
        let digest = pieces.next()?;
        let name = pieces.next()?.trim_start_matches('*');
        (name == asset
            && pieces.next().is_none()
            && digest.len() == 64
            && digest.bytes().all(|v| v.is_ascii_hexdigit()))
        .then(|| digest.to_ascii_lowercase())
    });
    let digest = matches.next()?;
    matches.next().is_none().then_some(digest)
}

pub fn atomic(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let temp = path.with_extension("write");
    let mut options = OpenOptions::new();
    options.create(true).truncate(true).write(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut f = options.open(&temp)?;
    f.write_all(bytes)?;
    f.sync_all()?;
    fs::rename(&temp, path)?;
    sync_dir(path.parent().unwrap_or(Path::new(".")))
}

pub fn sync_dir(path: &Path) -> io::Result<()> {
    #[cfg(unix)]
    File::open(path)?.sync_all()?;
    #[cfg(not(unix))]
    let _ = path;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn checksums_require_the_exact_asset_and_reject_duplicates() {
        let hex = "f".repeat(64);
        assert_eq!(
            checksum(&format!("{hex} *jarvisd-arm64\n"), "jarvisd-arm64"),
            Some(hex.clone())
        );
        assert!(checksum(&format!("{hex}  jarvisd-arm64-small"), "jarvisd-arm64").is_none());
        assert!(
            checksum(
                &format!("{hex} jarvisd-arm64\n{hex} jarvisd-arm64"),
                "jarvisd-arm64"
            )
            .is_none()
        );
    }
}
