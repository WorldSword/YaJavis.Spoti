# Rust-компоненты YaJarvis

`core` запускает ассистента, панель и плагины, `modules` содержит подключения и обучение, `dsp` обрабатывает звук. `setup` устанавливает пакет на колонку. `station-runtime` запускает ядро и проверяет обновления после перезапуска.

Перед командами Cargo соберите интерфейсы из корня репозитория через `npm ci` и `npm run build`.

```sh
cargo test --locked --workspace --all-features
cargo check --locked -p jarvis --no-default-features
cargo build --locked --release -p jarvis-setup
```

Сборка для колонки описана в [документации](../docs/BUILD.md).
