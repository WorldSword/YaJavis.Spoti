#!/bin/sh
# Сборка ядра на Rust под колонку (aarch64, статически, musl).
#   scripts/rs-build.sh [версия]      → build/jarvis-arm64
# Нужно: rustup с целью aarch64-unknown-linux-musl. Компоновщик — встроенный rust-lld (jarvis/.cargo/config.toml).
# C-код (когда появится: Lua, Opus) собирает zig: CC_aarch64_unknown_linux_musl="zig cc -target aarch64-linux-musl".
set -eu
cd "$(dirname "$0")/.."
VER="${1:-dev}"
export JARVIS_VERSION="$VER"
if [ -z "${JARVIS_UI:-}" ]; then
    npm run build:panel
fi
# C-код (ring): zig. cc-rs добавляет --target=aarch64-unknown-linux-musl, а zig такой триплет не понимает,
# поэтому нужна обёртка (ZCC), которая его убирает и ставит -target aarch64-linux-musl.
if [ -n "${ZCC:-}" ]; then
	export CC_aarch64_unknown_linux_musl="$ZCC" AR_aarch64_unknown_linux_musl="${ZAR:-$ZCC}"
fi
# Opus uses CMake. Explicitly select Linux when cross-compiling on Windows.
if [ -n "${ZCC:-}" ]; then
    export CMAKE_TOOLCHAIN_FILE_aarch64_unknown_linux_musl="$PWD/scripts/aarch64-musl.cmake"
    export CMAKE_GENERATOR_aarch64_unknown_linux_musl=Ninja
fi
set -- --release --target aarch64-unknown-linux-musl
if [ "${JARVIS_NO_DEFAULT_FEATURES:-0}" = "1" ]; then
    set -- "$@" --no-default-features
fi
if [ -n "${JARVIS_FEATURES:-}" ]; then
    set -- "$@" --features "$JARVIS_FEATURES"
fi
(cd jarvis && cargo build "$@")
mkdir -p build
cp jarvis/target/aarch64-unknown-linux-musl/release/jarvis build/jarvis-arm64
ls -l build/jarvis-arm64
