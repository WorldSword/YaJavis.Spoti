# Сборка YaJarvis

Для сборки пакетов колонки нужны Git, актуальный стабильный Rust, Zig, CMake, Ninja, Python 3 и tar. Сборка официальных плагинов под ARMv7 также использует libclang для привязок QuickJS.

### Веб-интерфейсы

Для сборки интерфейсов используйте Node.js 24, как в CI. Из корня репозитория запустите `npm ci`, затем одну из команд.

| Команда | Результат |
| --- | --- |
| `npm run build:installer` | Установщик в `build/ui/installer` |
| `npm run build:panel` | Панель колонки в `build/ui/panel` |
| `npm run build` | Оба интерфейса |

Cargo встраивает эти файлы в соответствующий бинарник. Если запускаете Cargo напрямую, сначала соберите нужный интерфейс. Для тестов всего Cargo workspace соберите оба. После изменения исходников интерфейса повторите его сборку перед запуском Cargo. `build.sh`, `build.ps1` и `scripts/rs-build.sh` собирают панель сами после установки npm-зависимостей. Node.js нужен только для сборки.

### Linux и macOS

Для `build.sh` также нужны POSIX shell, zip и sha256sum. На macOS команда `sha256sum` входит в GNU coreutils. Из корня проекта выполните следующие команды.

```sh
rustup target add aarch64-unknown-linux-musl armv7-unknown-linux-musleabihf
npm ci
VERSION=0.10.0 sh build.sh
npm run build:installer
cargo test --manifest-path jarvis/Cargo.toml --locked --workspace --all-features
cargo build --manifest-path jarvis/Cargo.toml --locked --release -p jarvis-setup
```

### Windows (PowerShell)

`build.ps1` запускает сборку пакетов колонки инструментами Windows. Добавьте их в `PATH`. Для Rust с целевой платформой MSVC установите Visual Studio Build Tools с компонентами C++. При сборке плагинов ARMv7 скрипт ищет `libclang.dll` в стандартном каталоге LLVM (`C:\Program Files\LLVM\bin`). Если LLVM установлен в другом месте, задайте `LIBCLANG_PATH` с путём к каталогу, содержащему `libclang.dll`.

Из корня проекта выполните следующие команды.

```powershell
rustup target add aarch64-unknown-linux-musl armv7-unknown-linux-musleabihf
npm ci
$env:VERSION = '0.10.0'
.\build.ps1
npm run build:installer
cargo test --manifest-path .\jarvis\Cargo.toml --locked --workspace --all-features
cargo build --manifest-path .\jarvis\Cargo.toml --locked --release -p jarvis-setup
```

### Пакеты и локальная установка

Сборка создаёт один пакет чистой прошивки на архитектуру и отдельный архив для каждого официального плагина. Каталог указывает, какие пакеты можно скачать, а пользователь сам выбирает их в панели.

После чистой установки колонка сама проверяет и применяет опубликованные обновления ядра, если автообновление включено. Файлы установленных плагинов и их данные находятся в отдельных каталогах.

`build/release` содержит ZIP-пакеты прошивки, ядра для обновления, пакеты плагинов и контрольные суммы. GitHub Actions собирает установщики для Windows, Linux и macOS. Для сборки под одну архитектуру задайте в окружении `STATION_ARCHES` со значением `arm64` или `armv7`. Для своего репозитория задайте `REPO` в формате `owner/repo`.

Установщик ищет `adb` рядом с собой, в PATH и папках Android SDK. На Windows, macOS и Linux x86-64 он скачает Android Platform Tools с сайта Google, если ADB ещё нет. Чтобы указать свой `adb`, задайте `JARVIS_ADB` с полным путём.

При ручном подключении можно указать адрес с портом, например `192.168.1.50:5556`. Если ввести только IP, установщик использует порт ADB `5555`. Автоматический поиск проверяет `5555` и показывает уже подключённые к ADB колонки с другими портами.

Если веб-панель проброшена на внешний порт, отличный от `8080`, запустите установщик с флагом `--skip-panel-check`, например `./JarvisSetup-windows-amd64.exe --skip-panel-check`. Установщик дождётся подтверждения запуска по ADB. После установки откройте панель по своему адресу.

Если Rust-версия ещё не опубликована в GitHub Releases, запустите локально собранный установщик с путём к каталогу ZIP-пакетов. Команды ниже выполняются из корня проекта.

**Linux и macOS**

```sh
JARVIS_SETUP_PAYLOAD="$PWD/build/release" ./jarvis/target/release/jarvis-setup
```

**Windows (PowerShell)**

```powershell
$env:JARVIS_SETUP_PAYLOAD = (Resolve-Path .\build\release).Path
.\jarvis\target\release\jarvis-setup.exe
```

Установщик сам выберет `station-arm64.zip` или `station-armv7.zip` по архитектуре системы колонки. Для каталогов с локальными пакетами сверяйте ZIP-файлы с `SHA256SUMS` перед установкой.

| Каталог | Содержимое |
| --- | --- |
| `jarvis/core` | Rust-ядро, панель, запуск модулей и обновление |
| `jarvis/modules` | Rust-модули подключений, музыки и обучения |
| `jarvis/station-runtime` | Запуск ядра и откат обновления на колонке |
| `jarvis/setup` | Rust-установщик для компьютера |
| `jarvisd/ui`, `installer/ui` | Веб-интерфейсы ядра и установщика |
| `station` | Скрипт автозапуска и нативные аудиоадаптеры прошивки |
| `plugins` | Манифесты модулей и Lua-плагины |
| `models`, `kws` | Модели слова и инструменты подготовки обучающих данных |

### Официальные плагины из локальной сборки

Чтобы кнопка «Установить» в официальном каталоге скачивала локальные архивы Rust-плагинов, задайте `JARVIS_PLUGIN_ASSETS` перед сборкой ядра. Адрес вшивается в `jarvisd`. В примерах замените `192.168.31.10` на IP машины сборки, доступный с колонки или из гостя КУЯСЭ. Пример собирает пакеты для ядра ARM64. Для ядра ARMv7 укажите `armv7`.

Выполните сборку из корня проекта.

**Windows (PowerShell)**

```powershell
$env:JARVIS_PLUGIN_ASSETS = 'http://192.168.31.10:18079'
$env:STATION_ARCHES = 'arm64'
.\build.ps1
```

**Linux и macOS**

```sh
JARVIS_PLUGIN_ASSETS=http://192.168.31.10:18079 STATION_ARCHES=arm64 sh build.sh
```

В отдельном терминале из корня проекта запустите HTTP-сервер. Из `build/release` ядро скачивает архивы `plugin-<id>-<arch>.zip` и проверяет их по `SHA256SUMS`.

**Windows (PowerShell)**

```powershell
python -m http.server 18079 --bind 192.168.31.10 --directory .\build\release
```

**Linux и macOS**

```sh
python3 -m http.server 18079 --bind 192.168.31.10 --directory ./build/release
```

Установите новый пакет ядра, указав каталог `build/release` через `JARVIS_SETUP_PAYLOAD` по инструкции [локальной установки](#пакеты-и-локальная-установка). Если веб-панель КУЯСЭ проброшена на другой порт, запустите установщик с `--skip-panel-check`. Оставьте HTTP-сервер запущенным, пока устанавливаете плагины из каталога.

Список официальных плагинов и исходники для автоматической установки Lua-плагинов загружаются с GitHub. Локальные Lua-архивы, например `plugin-weather.zip`, можно загрузить через «Подключения → Плагины → Плагин из файла».
