#!/bin/sh
APP_PATH="$1"
[ -n "$APP_PATH" ] || APP_PATH="$(dirname "$0")"
DATA_PATH="$(dirname "$APP_PATH")/_meteoapi"
WEB_PATH="/var/www/meteoapi"
mkdir -p "$DATA_PATH/cache" "$DATA_PATH/history" "$DATA_PATH/local"
chmod 700 "$DATA_PATH" "$DATA_PATH/cache" "$DATA_PATH/history" "$DATA_PATH/local" 2>/dev/null || true
chmod 755 "$APP_PATH/bin/meteoapi" 2>/dev/null || true
rm -rf "$WEB_PATH"
ln -s "$APP_PATH/web" "$WEB_PATH"
exit 0
