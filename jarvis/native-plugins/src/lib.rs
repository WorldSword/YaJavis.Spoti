pub use jarvis_plugin_sdk::*;

/// Add registrations for the crates selected in this firmware build.
pub static PLUGINS: &[Registration] = &[
    #[cfg(feature = "example")]
    example::PLUGIN,
];

#[cfg(feature = "example")]
mod example;
