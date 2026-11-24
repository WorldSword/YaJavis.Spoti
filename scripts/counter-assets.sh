#!/bin/sh
# Файлы счётчика установок в служебном релизе «stats».
# Колонка скачивает new-<модель> один раз и v<версия>-<модель> один раз на версию; GitHub считает скачивания.
# Уже загруженные файлы не трогаем — иначе обнулится их счётчик.
set -eu
V=${1:?версия, например 0.9.53}
MODELS="lite lite2 mini1 mini2 mini3 mini3pro midi station2 station3 street fiero other"
if ! gh release view stats >/dev/null 2>&1; then
    gh release create stats --prerelease --title "Счётчик установок (служебный)" \
        --notes "Служебный релиз. Колонки с YaJarvis скачивают отсюда пустые файлы, а GitHub считает скачивания — так в README видно, сколько колонок с Джарвисом. Передаются только модель и версия. Скачивать здесь ничего не нужно."
fi
have=$(gh release view stats --json assets -q '.assets[].name')
dir=$(mktemp -d)
files=
for m in $MODELS; do
    for n in "new-$m" "v$V-$m"; do
        if printf '%s\n' "$have" | grep -qx "$n"; then continue; fi
        printf 'ok\n' > "$dir/$n"
        files="$files $dir/$n"
    done
done
# shellcheck disable=SC2086
[ -z "$files" ] || gh release upload stats $files
echo "счётчик: файлы для $V готовы"
