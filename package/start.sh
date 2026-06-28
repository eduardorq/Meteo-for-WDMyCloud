#!/bin/sh
APP_PATH="$1"
[ -n "$APP_PATH" ] || APP_PATH="$(dirname "$0")"
APP_PATH="$(cd "$APP_PATH" 2>/dev/null && pwd)"
DATA_PATH="$(dirname "$APP_PATH")/_meteoapi"
PID_FILE="$DATA_PATH/meteoapi.pid"
LOG_FILE="$DATA_PATH/launcher.log"
BIN="$APP_PATH/bin/meteoapi"
mkdir -p "$DATA_PATH/cache" "$DATA_PATH/history" "$DATA_PATH/local"
chmod 700 "$DATA_PATH" "$DATA_PATH/cache" "$DATA_PATH/history" "$DATA_PATH/local" 2>/dev/null || true
chmod 755 "$BIN" 2>/dev/null || true
umask 077
if [ -f "$PID_FILE" ]; then
    PID="$(cat "$PID_FILE" 2>/dev/null)"
    if [ -n "$PID" ] && [ -d "/proc/$PID" ]; then
        EXE="$(readlink "/proc/$PID/exe" 2>/dev/null)"
        [ "$EXE" = "$BIN" ] && exit 0
    fi
    rm -f "$PID_FILE"
fi
if [ ! -x "$BIN" ]; then
    echo "$(date) ERROR: no se encuentra el binario $BIN" >> "$LOG_FILE"
    exit 1
fi
HOME="$DATA_PATH"
export HOME
if command -v nohup >/dev/null 2>&1; then
    nohup "$BIN" --data-dir "$DATA_PATH" --listen "0.0.0.0:8098" >> "$LOG_FILE" 2>&1 </dev/null &
else
    "$BIN" --data-dir "$DATA_PATH" --listen "0.0.0.0:8098" >> "$LOG_FILE" 2>&1 </dev/null &
fi
PID=$!
echo "$PID" > "$PID_FILE"
sleep 2
if [ ! -d "/proc/$PID" ]; then
    echo "$(date) WARNING: MeteoAPI terminó durante el arranque; revise $LOG_FILE y meteoapi.log" >> "$LOG_FILE"
    rm -f "$PID_FILE"
    exit 0
fi

# A request exercises the HTTP middleware too.  This catches runtime faults
# that would otherwise appear in the browser only as ERR_EMPTY_RESPONSE.
if command -v wget >/dev/null 2>&1; then
    if ! wget -q -T 5 -O /dev/null "http://127.0.0.1:8098/"; then
        echo "$(date) WARNING: el proceso está vivo pero el panel HTTP no superó la comprobación local" >> "$LOG_FILE"
    fi
fi
exit 0
