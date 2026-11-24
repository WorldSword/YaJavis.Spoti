use super::{Scratch, token};
use std::{
    env, fs,
    io::{self, Cursor, Read},
    path::{Path, PathBuf},
    time::Duration,
};

const ADB: &str = if cfg!(windows) { "adb.exe" } else { "adb" };
const MAX_ARCHIVE: usize = 48 << 20;
#[cfg(windows)]
const BINARIES: &[&str] = &["adb.exe", "AdbWinApi.dll", "AdbWinUsbApi.dll"];
#[cfg(not(windows))]
const BINARIES: &[&str] = &["adb"];

pub async fn prepare() -> Result<PathBuf, String> {
    if let Some(path) = env::var_os("JARVIS_ADB").filter(|value| !value.is_empty()) {
        return resolve_override(path.into());
    }
    if let Some(path) = installed() {
        return Ok(path);
    }
    let cache =
        cache_dir().ok_or("Не удалось найти папку пользователя для Android Platform Tools")?;
    if complete(&cache) {
        return Ok(cache.join(ADB));
    }
    download(&cache).await
}

fn resolve_override(path: PathBuf) -> Result<PathBuf, String> {
    file(&path).then(|| path.clone()).ok_or_else(|| {
        format!(
            "JARVIS_ADB указывает на отсутствующий файл ({})",
            path.display()
        )
    })
}

fn installed() -> Option<PathBuf> {
    let mut dirs = Vec::new();
    if let Ok(exe) = env::current_exe()
        && let Some(dir) = exe.parent()
    {
        dirs.extend([dir.to_path_buf(), dir.join("platform-tools")]);
    }
    if let Some(path) = env::var_os("PATH") {
        dirs.extend(env::split_paths(&path));
    }
    for name in ["ANDROID_HOME", "ANDROID_SDK_ROOT"] {
        if let Some(dir) = env::var_os(name) {
            dirs.push(PathBuf::from(dir).join("platform-tools"));
        }
    }
    #[cfg(windows)]
    {
        if let Some(dir) = env::var_os("LOCALAPPDATA") {
            dirs.push(PathBuf::from(dir).join("Android/Sdk/platform-tools"));
        }
        let drive = env::var("SystemDrive").unwrap_or_else(|_| "C:".into());
        dirs.push(PathBuf::from(format!("{drive}\\lite-kit\\tools")));
    }
    #[cfg(not(windows))]
    if let Some(home) = env::var_os("HOME") {
        let home = PathBuf::from(home);
        dirs.extend([
            home.join("Android/Sdk/platform-tools"),
            home.join("Library/Android/sdk/platform-tools"),
        ]);
    }
    dirs.into_iter()
        .filter(|dir| dir.is_absolute())
        .map(|dir| dir.join(ADB))
        .find(|path| file(path))
}

fn cache_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    let base = env::var_os("LOCALAPPDATA").map(PathBuf::from);
    #[cfg(target_os = "macos")]
    let base = env::var_os("HOME").map(|home| PathBuf::from(home).join("Library/Caches"));
    #[cfg(not(any(windows, target_os = "macos")))]
    let base = env::var_os("XDG_CACHE_HOME")
        .filter(|dir| !dir.is_empty())
        .map(PathBuf::from)
        .or_else(|| env::var_os("HOME").map(|home| PathBuf::from(home).join(".cache")));
    base.filter(|path| path.is_absolute())
        .map(|path| path.join("YaJarvis/platform-tools"))
}

fn file(path: &Path) -> bool {
    path.metadata()
        .is_ok_and(|entry| entry.is_file() && entry.len() > 0)
}

fn complete(dir: &Path) -> bool {
    BINARIES.iter().all(|name| file(&dir.join(name)))
}

async fn download(cache: &Path) -> Result<PathBuf, String> {
    let platform = if cfg!(windows) {
        "windows"
    } else if cfg!(target_os = "macos") {
        "darwin"
    } else if cfg!(all(target_os = "linux", target_arch = "x86_64")) {
        "linux"
    } else {
        return Err("Установите ADB для вашей системы и добавьте его в PATH".into());
    };
    let url =
        format!("https://dl.google.com/android/repository/platform-tools-latest-{platform}.zip");
    let mut response = reqwest::Client::builder()
        .connect_timeout(Duration::from_secs(12))
        .timeout(Duration::from_secs(90))
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .map_err(|error| error.to_string())?
        .get(url)
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|error| format!("Не удалось скачать ADB с сайта Google: {error}"))?;
    if !response.status().is_success() {
        return Err(format!(
            "Не удалось скачать ADB. Google ответил {}",
            response.status()
        ));
    }
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|error| error.to_string())? {
        if bytes.len() + chunk.len() > MAX_ARCHIVE {
            return Err("Архив Android Platform Tools слишком большой".into());
        }
        bytes.extend_from_slice(&chunk);
    }
    let cache = cache.to_path_buf();
    tokio::task::spawn_blocking(move || install(&cache, &bytes))
        .await
        .map_err(|error| error.to_string())?
}

fn install(cache: &Path, bytes: &[u8]) -> Result<PathBuf, String> {
    let parent = cache
        .parent()
        .ok_or("Некорректный путь до Android Platform Tools")?;
    fs::create_dir_all(parent).map_err(|error| error.to_string())?;
    let staged = parent.join(format!("platform-tools-{}", token()?));
    fs::create_dir(&staged).map_err(|error| error.to_string())?;
    let scratch = Scratch(staged);
    let mut zip = zip::ZipArchive::new(Cursor::new(bytes)).map_err(|error| error.to_string())?;
    for name in BINARIES.iter().copied().chain(["NOTICE.txt"]) {
        let mut entry = match zip.by_name(&format!("platform-tools/{name}")) {
            Ok(entry) => entry,
            Err(zip::result::ZipError::FileNotFound) if name == "NOTICE.txt" => continue,
            Err(error) => {
                return Err(format!(
                    "В архиве Android Platform Tools нет {name}: {error}"
                ));
            }
        };
        if !entry.is_file()
            || entry.is_symlink()
            || entry.size() == 0
            || entry.size() > MAX_ARCHIVE as u64
        {
            return Err(format!("Некорректный файл Android Platform Tools ({name})"));
        }
        let path = scratch.0.join(name);
        let mut options = fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(if name == ADB { 0o700 } else { 0o600 });
        }
        let mut output = options.open(path).map_err(|error| error.to_string())?;
        let size = entry.size();
        let written = io::copy(
            &mut entry.by_ref().take(MAX_ARCHIVE as u64 + 1),
            &mut output,
        )
        .map_err(|error| error.to_string())?;
        if written != size {
            return Err(format!("Файл Android Platform Tools повреждён ({name})"));
        }
    }
    if let Err(error) = fs::rename(&scratch.0, cache)
        && !complete(cache)
    {
        return Err(format!(
            "Не удалось сохранить Android Platform Tools: {error}"
        ));
    }
    Ok(cache.join(ADB))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    #[ignore = "проверяет актуальный архив Android Platform Tools с сайта Google"]
    async fn downloads_official_platform_tools() {
        let scratch = Scratch::new().unwrap();
        let cache = scratch.0.join("platform-tools");
        let adb = download(&cache).await.unwrap();
        assert_eq!(adb, cache.join(ADB));
        assert!(complete(&cache));
        let version = tokio::process::Command::new(adb)
            .arg("version")
            .output()
            .await
            .unwrap();
        assert!(version.status.success());
        assert!(String::from_utf8_lossy(&version.stdout).contains("Android Debug Bridge"));
    }
}
