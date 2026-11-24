use std::{
    fs,
    io::{self, Write},
    path::Path,
};

pub fn rand_hex(n: usize) -> String {
    let mut random = vec![0u8; n];
    getrandom::fill(&mut random).expect("случайные числа");
    random.iter().map(|byte| format!("{byte:02x}")).collect()
}

pub fn write_atomic(path: &Path, bytes: &[u8], mode: u32) -> io::Result<()> {
    let tmp = path.with_extension("tmp");
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(mode);
    }
    #[cfg(not(unix))]
    let _ = mode;
    let mut file = options.open(&tmp)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    drop(file);
    fs::rename(&tmp, path)
}

pub fn free_bytes(path: &Path) -> Option<u64> {
    #[cfg(unix)]
    {
        use std::os::unix::ffi::OsStrExt;
        for candidate in path.ancestors() {
            if let Ok(path) = std::ffi::CString::new(candidate.as_os_str().as_bytes()) {
                let mut status = std::mem::MaybeUninit::<libc::statvfs>::uninit();
                if unsafe { libc::statvfs(path.as_ptr(), status.as_mut_ptr()) } == 0 {
                    let status = unsafe { status.assume_init() };
                    return Some((status.f_bavail as u64).saturating_mul(status.f_frsize as u64));
                }
            }
        }
        None
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        None
    }
}

#[allow(dead_code)]
pub fn free_mb(path: &Path) -> u64 {
    free_bytes(path).map_or(0, |bytes| bytes >> 20)
}

pub fn user_agent() -> String {
    format!(
        "jarvis-training/{}",
        option_env!("JARVIS_VERSION").unwrap_or("dev")
    )
}

pub fn short(error: &reqwest::Error) -> String {
    error.to_string().chars().take(140).collect()
}
