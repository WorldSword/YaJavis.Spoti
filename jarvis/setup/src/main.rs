mod installer;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut skip_panel_check = false;
    for argument in std::env::args_os().skip(1) {
        match argument.to_str() {
            Some("--skip-panel-check") => skip_panel_check = true,
            Some("-h" | "--help") => {
                println!("Использование: jarvis-setup [--skip-panel-check]");
                println!("  --skip-panel-check  Пропустить HTTP-проверку панели с компьютера");
                return Ok(());
            }
            _ => return Err(format!("Неизвестный аргумент {argument:?}. См. --help.").into()),
        }
    }
    installer::run(skip_panel_check).await
}
