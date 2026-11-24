//! Журнал: «2006/01/02 15:04:05 текст» в часовом поясе хозяина (часы колонки идут по Гринвичу).
//! Формат тот же, что у Go-версии: панель («Журнал») и jarvisd.sh читают его одинаково.

use std::{
    io::Write,
    sync::{OnceLock, RwLock},
};

static TZ: OnceLock<RwLock<jiff::tz::TimeZone>> = OnceLock::new();

fn tz() -> &'static RwLock<jiff::tz::TimeZone> {
    TZ.get_or_init(|| RwLock::new(jiff::tz::TimeZone::UTC))
}

pub fn set_timezone(name: &str) {
    if let Ok(z) = jiff::tz::TimeZone::get(name) {
        *tz().write().unwrap() = z;
    }
}

struct Logger;

impl log::Log for Logger {
    fn enabled(&self, m: &log::Metadata) -> bool {
        m.level() <= log::Level::Info && m.target().starts_with("jarvis")
    }

    fn log(&self, r: &log::Record) {
        if !self.enabled(r.metadata()) {
            return;
        }
        let now = jiff::Timestamp::now().to_zoned(tz().read().unwrap().clone());
        let line = format!("{} {}\n", now.strftime("%Y/%m/%d %H:%M:%S"), r.args());
        let _ = std::io::stderr().lock().write_all(line.as_bytes());
    }

    fn flush(&self) {}
}

pub fn init() {
    static L: Logger = Logger;
    let _ = log::set_logger(&L);
    log::set_max_level(log::LevelFilter::Info);
}
