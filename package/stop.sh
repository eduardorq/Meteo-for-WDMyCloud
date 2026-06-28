#!/bin/sh
APP_PATH="$1"
[ -n "$APP_PATH" ] || APP_PATH="$(dirname "$0")"
APP_PATH="$(cd "$APP_PATH" 2>/dev/null && pwd)"
DATA_PATH="$(dirname "$APP_PATH")/_meteoapi"
PID_FILE="$DATA_PATH/meteoapi.pid"
BIN="$APP_PATH/bin/meteoapi"
if [ -f "$PID_FILE" ]; then
    PID="$(cat "$PID_FILE" 2>/dev/null)"
    if [ -n "$PID" ] && [ -d "/proc/$PID" ]; then
        EXE="$(readlink "/proc/$PID/exe" 2>/dev/null)"
        if [ "$EXE" = "$BIN" ]; then
            kill "$PID" 2>/dev/null || true
            COUNT=0
            while [ -d "/proc/$PID" ] && [ "$COUNT" -lt 15 ]; do
                sleep 1
                COUNT=$((COUNT + 1))
            done
            [ -d "/proc/$PID" ] && kill -9 "$PID" 2>/dev/null || true
        fi
    fi
    rm -f "$PID_FILE"
fi
exit 0
