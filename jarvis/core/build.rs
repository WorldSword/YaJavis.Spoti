use std::io::Write;

#[path = "../embed_ui.rs"]
mod embed_ui;

fn main() {
    embed_ui::embed_ui("panel", "JARVIS_UI", Some(gzip));
}

// Браузер распаковывает gzip сам, а панель занимает меньше места в бинарнике.
fn gzip(bytes: &[u8]) -> Vec<u8> {
    let mut encoder = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::best());
    encoder.write_all(bytes).unwrap();
    encoder.finish().unwrap()
}
