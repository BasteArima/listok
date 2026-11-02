#!/bin/sh
# Подписать команду для iPad. Только на Mac, нужен вход в iCloud: подпись делает сервис Apple.
# После этого закоммитить shortcut/listok-signed.shortcut: сервис отдаёт его на вкладке «Подключения».
set -e
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
plutil -convert binary1 -o "$tmp/listok.shortcut" listok.plist
shortcuts sign --mode anyone --input "$tmp/listok.shortcut" --output listok-signed.shortcut
echo "Готово: shortcut/listok-signed.shortcut"
