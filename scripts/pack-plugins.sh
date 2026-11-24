#!/bin/sh
set -eu
out=${1:?Pass the output directory}
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$(dirname "$0")/../plugins"
for id in weather currency dice yandexhome; do
    path="$out/plugin-$id.zip"
    rm -f "$path"
    (cd "$id" && zip -q "$path" plugin.json main.lua)
done
