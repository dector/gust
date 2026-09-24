#!/usr/bin/env bash
#
# dev:self - external development supervisor for Gust itself.
#
# Builds Gust from source, runs it against the docs/demo app, and watches the
# Gust sources. A source change triggers a rebuild first; the running Gust is
# stopped only after a successful build. A failed build leaves the running
# Gust untouched.
#
# Watch scope is limited to compiled Go sources and module files, so build
# artifacts (out/, ~/.cache/go-build) and demo edits (docs/demo, handled by
# Gust's own watcher) do not restart the supervisor.
#
# Usage: tools/dev-self.sh [extra gust flags...]
#
# Environment overrides (defaults match the shared VPS dev setup):
#   DEV_SELF_PORTS      port spec, default 8000:8001
#   DEV_SELF_TAILSCALE  1 to pass -T, default 1
#   DEV_SELF_POLL       poll interval in seconds, default 1
#   DEV_SELF_DEBOUNCE   settle delay after a change, default 0.4
#   DEV_SELF_STOP_TIMEOUT  graceful stop timeout in seconds, default 5

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
DEMO_DIR="$ROOT/docs/demo"
OUT_DIR="$ROOT/out"
BIN="$OUT_DIR/gust"

PORT_SPEC="${DEV_SELF_PORTS:-8000:8001}"
WANT_TAILSCALE="${DEV_SELF_TAILSCALE:-1}"
POLL_INTERVAL="${DEV_SELF_POLL:-1}"
DEBOUNCE="${DEV_SELF_DEBOUNCE:-0.4}"
STOP_TIMEOUT="${DEV_SELF_STOP_TIMEOUT:-5}"

EXTRA_ARGS=("$@")

GUST_PID=""
TMP_BIN=""
STOPPING=0
PREVIOUS_CTRL_C=0
INTERACTIVE=0

log() { printf '[dev:self] %s\n' "$*" >&2; }

# snapshot prints a deterministic fingerprint of the inputs compiled into Gust:
# Go sources, embedded docs, and module files. docs/demo is excluded so the
# demo's own reload loop is not disturbed.
snapshot() {
  {
    find "$ROOT/cmd" "$ROOT/internal" -type f -name '*.go' -printf '%T@ %s %p\n' 2>/dev/null
    [ -f "$ROOT/docs/embed.go" ] && stat -c '%Y %s %n' "$ROOT/docs/embed.go" 2>/dev/null
    [ -d "$ROOT/docs/man" ] && find "$ROOT/docs/man" -type f -printf '%T@ %s %p\n' 2>/dev/null
    for f in go.mod go.sum; do
      [ -f "$ROOT/$f" ] && stat -c '%Y %s %n' "$ROOT/$f" 2>/dev/null
    done
    :
  } | LC_ALL=C sort
}

# build compiles a fresh binary to a temporary path and swaps it in atomically.
# The running binary keeps executing from the old inode, so a failure never
# disturbs the current Gust.
build() {
  mkdir -p "$OUT_DIR"
  TMP_BIN="$OUT_DIR/.gust.$$.$RANDOM"
  if ( cd "$ROOT" && go build -o "$TMP_BIN" ./cmd/gust ); then
    mv -f "$TMP_BIN" "$BIN"
    TMP_BIN=""
    return 0
  fi
  rm -f "$TMP_BIN"
  TMP_BIN=""
  return 1
}

stop_gust() {
  local pid="$GUST_PID"
  [ -n "$pid" ] || return 0
  GUST_PID=""

  if ! kill -0 "$pid" 2>/dev/null; then
    wait "$pid" 2>/dev/null || true
    return 0
  fi

  log "stopping Gust (pid $pid)"
  kill -TERM "$pid" 2>/dev/null || true

  # wait does not support a timeout, so a watchdog escalates to SIGKILL.
  (
    sleep "$STOP_TIMEOUT"
    kill -KILL "$pid" 2>/dev/null || true
  ) &
  local watchdog=$!
  wait "$pid" 2>/dev/null || true
  kill "$watchdog" 2>/dev/null || true
  wait "$watchdog" 2>/dev/null || true
}

start_gust() {
  [ -x "$BIN" ] || { log "no binary at $BIN"; return 1; }

  local args=(
    -e "uv run server.py"
    -p "$PORT_SPEC"
    -h /health
    --exclude .venv
    --optin comments
    --self-dev
  )
  [ "$WANT_TAILSCALE" = "1" ] && args+=(-T)
  args+=(${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"})

  log "starting Gust on $PORT_SPEC (demo: $DEMO_DIR)"
  if [ "$INTERACTIVE" -eq 1 ]; then
    (
      cd "$DEMO_DIR" || exit 1
      DEV_SELF_PREVIOUS_CTRL_C="$PREVIOUS_CTRL_C" exec python3 "$SCRIPT_DIR/dev-self-tty.py" "$BIN" "${args[@]}" </dev/tty
    ) &
  else
    (
      cd "$DEMO_DIR" || exit 1
      exec "$BIN" "${args[@]}" </dev/null
    ) &
  fi
  GUST_PID=$!
  log "Gust pid $GUST_PID"
}

cleanup() {
  STOPPING=1
  stop_gust
  [ -n "$TMP_BIN" ] && rm -f "$TMP_BIN"
}

on_signal() {
  if [ "$STOPPING" -eq 0 ]; then
    STOPPING=1
    log "signal received, shutting down"
    stop_gust
  fi
  exit 0
}

main() {
  command -v go >/dev/null 2>&1 || { log "go is required"; exit 1; }
  [ -d "$DEMO_DIR" ] || { log "missing demo directory: $DEMO_DIR"; exit 1; }
  command -v uv >/dev/null 2>&1 || log "warning: uv not found; the demo app may fail to start"
  if [ -t 1 ] && ( : </dev/tty ) 2>/dev/null; then
    command -v python3 >/dev/null 2>&1 || { log "python3 is required for keyboard controls"; exit 1; }
    INTERACTIVE=1
  fi

  trap on_signal INT TERM HUP
  trap cleanup EXIT

  local sig
  sig="$(snapshot)"

  if build; then
    start_gust
  else
    log "initial build failed; watching for fixes"
  fi

  while [ "$STOPPING" -eq 0 ]; do
    sleep "$POLL_INTERVAL" || true
    [ "$STOPPING" -eq 0 ] || break

    if [ -n "$GUST_PID" ] && ! kill -0 "$GUST_PID" 2>/dev/null; then
      local exit_code=0
      wait "$GUST_PID" || exit_code=$?
      GUST_PID=""
      if [ "$exit_code" -eq 43 ]; then
        log "second Ctrl+C: stopping supervisor"
        break
      fi
      if [ "$exit_code" -eq 42 ]; then
        PREVIOUS_CTRL_C=1
        log "Ctrl+C: restarting Gust (press Ctrl+C again to stop dev:self)"
      else
        PREVIOUS_CTRL_C=0
        log "Gust exited; restarting"
      fi
      start_gust
    fi

    local current
    current="$(snapshot)"
    [ "$current" = "$sig" ] && continue

    # Let editors finish writing before we build.
    local settled
    while :; do
      sleep "$DEBOUNCE"
      settled="$(snapshot)"
      [ "$settled" = "$current" ] && break
      current="$settled"
    done
    sig="$current"

    log "source change detected; rebuilding"
    if build; then
      stop_gust
      PREVIOUS_CTRL_C=0
      start_gust
    else
      log "build failed; keeping the running Gust"
    fi
  done
}

main
