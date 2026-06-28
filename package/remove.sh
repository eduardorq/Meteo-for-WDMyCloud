#!/bin/sh
APP_PATH="$1"
[ -n "$APP_PATH" ] || APP_PATH="$(dirname "$0")"
# Se conserva _meteoapi para no perder configuración ni histórico al reinstalar.
rm -rf "$APP_PATH"
exit 0
