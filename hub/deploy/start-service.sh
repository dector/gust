#!/bin/sh
set -eu

if [ -z "${APP_PASSWORD:-}" ]; then
  echo 'Hub service requires a nonempty APP_PASSWORD in ~/.config/hub/hub.env' >&2
  exit 1
fi

APP_HOST=127.0.0.1 APP_PORT=2222 exec ./bin/server
