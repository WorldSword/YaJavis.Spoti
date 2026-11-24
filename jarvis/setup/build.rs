#[path = "../embed_ui.rs"]
mod embed_ui;

fn main() {
    embed_ui::embed_ui("installer", "JARVIS_SETUP_UI", None);
}
