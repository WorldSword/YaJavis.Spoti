#!/bin/sh
# Public release build. Never reads local settings or .env files.
set -eu
cd "$(dirname "$0")"
VERSION=${VERSION:-0.9.68}
VERSION=${VERSION#v}
REPO=${REPO:-newfpv/YaJavis}
GO=${GO:-go}
ZIG=${ZIG:-zig}
P=installer/payload
mkdir -p build "$P"
"$GO" run scripts/pack-google.go
trap 'rm -f jarvisd/google_app_generated.go' EXIT
LD="-s -w -buildid= -X main.version=$VERSION -X main.defaultRepo=$REPO"
for arch in arm64 arm; do
    name=arm64
    [ "$arch" != arm ] || name=armv7
    (cd jarvisd && CGO_ENABLED=0 GOOS=linux GOARCH=$arch GOARM=7 "$GO" build -trimpath -ldflags="$LD" -o "../$P/jarvisd-$name" .)
done
"$ZIG" cc -target arm-linux-gnueabihf.2.30 -mcpu=cortex_a7 -shared -fPIC -O2 -s -o "$P/vqe_tap-armhf.so" station/vqe_tap.c -ldl
"$ZIG" cc -target aarch64-linux-gnu.2.30 -shared -fPIC -O2 -s -o "$P/vqe_tap-arm64.so" station/vqe_tap.c -ldl
"$ZIG" cc -target arm-linux-gnueabihf.2.30 -mcpu=cortex_a7 -O2 -s -o "$P/spotcmd" station/spotcmd.c -ldl
tr -d '\r' < station/jarvisd.sh > "$P/jarvisd.sh"
tr -d '\r' < station/S98jarvisd > "$P/S98jarvisd"
cp models/wake.bin models/oww.bin models/wake_oww.bin "$P/"
(cd installer && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$GO" build -trimpath -ldflags="-s -w -buildid= -X main.version=$VERSION" -o ../build/JarvisSetup.exe .)
if [ "${RELEASE:-0}" = 1 ]; then
    R=build/release
    mkdir -p "$R"
    for t in windows/amd64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
        os=${t%/*}; arch=${t#*/}; ext=
        [ "$os" != windows ] || ext=.exe
        (cd installer && CGO_ENABLED=0 GOOS=$os GOARCH=$arch "$GO" build -trimpath -ldflags="-s -w -buildid= -X main.version=$VERSION" -o "../$R/JarvisSetup-$os-$arch$ext" .)
    done
    cp "$P/jarvisd-arm64" "$P/jarvisd-armv7" "$R/"
    sed "s#__REPO__#$REPO#g" installer/install.sh | tr -d '\r' > "$R/install.sh"
    sed "s#__REPO__#$REPO#g" installer/install.ps1 | tr -d '\r' > "$R/install.ps1"
    tar -czf "$R/THIRD_PARTY_NOTICES.tar.gz" LICENSE LICENSES THIRD_PARTY.md CREDITS.md
    "$GO" run scripts/pack-plugins.go "$R"   # официальные плагины: plugin-<id>.zip
    (cd "$R" && sha256sum JarvisSetup-* jarvisd-* install.* plugin-*.zip THIRD_PARTY_NOTICES.tar.gz > SHA256SUMS)
fi
