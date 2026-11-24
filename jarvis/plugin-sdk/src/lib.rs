//! Source-level interface for trusted modules linked into the YaJarvis executable.
pub use serde_json::{Map, Value, json};
#[cfg(feature = "process")]
pub mod process;
use std::{future::Future, path::PathBuf, pin::Pin, sync::Arc};

pub type Response<'a> = Pin<Box<dyn Future<Output = Result<Value, String>> + Send + 'a>>;
pub type Service = Arc<dyn Fn(String, Value) -> Response<'static> + Send + Sync>;

pub struct Context {
    pub data_dir: PathBuf,
    pub settings: Map<String, Value>,
    pub info: Arc<dyn Fn() -> Value + Send + Sync>,
    pub say: Arc<dyn Fn(String) + Send + Sync>,
    pub service: Service,
}

pub trait Plugin: Send + Sync + 'static {
    fn request(&self, method: &str, params: Value) -> Response<'_>;
    fn tools(&self) -> Vec<Value> {
        Vec::new()
    }
    fn configure(&self, _settings: Map<String, Value>) {}
    fn stop(&self) {}
}

pub struct Registration {
    pub manifest: &'static str,
    pub chat: bool,
    pub start: fn(Context) -> Result<Box<dyn Plugin>, String>,
}
