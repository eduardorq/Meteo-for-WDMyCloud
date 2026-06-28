#!/bin/sh
SRC="$1"
DEST="$2"
[ -n "$SRC" ] && [ -n "$DEST" ] || exit 1
mv "$SRC" "$DEST"
exit $?
