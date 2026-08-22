# Gust v1 Specification

## Summary

Gust is a Linux-first local development runner. It watches the current project, reruns a user command on changes or manual triggers, streams app logs to the terminal, exposes an agent socket, and can optionally run a browser-reload proxy.

V1 is intentionally small: one command, one optional app port, one optional proxy port, flags only, no config file.

## Goals

- Run a developer command immediately and rerun it on demand.
- Watch the filesystem efficiently on Linux.
- Support manual rerun from the terminal with `r`.
- Support agent-triggered rerun/status through a Unix socket.
- Stream child stdout/stderr raw to the terminal.
- Optionally proxy app traffic and inject browser reload code.
- Delay browser reload until the app is ready.
- Show simple browser error banners for app lifecycle failures.

## CLI

```sh
gust -e 'go run ./cmd/server'
gust -e 'go run ./cmd/server' -p 8080
gust -e 'go run ./cmd/server' -p 8080:5000
gust -e 'go run ./cmd/server' -p 8080:5000 -h /health
gust -e 'go run ./cmd/server' -p 8080 --exclude frontend/node_modules -v
```

Flags:

```text
-e <cmd>     required command, executed via /bin/sh -c
-p <port>    optional app port, or app:proxy ports
-h <path>    optional health endpoint, requires app port
--exclude         repeatable path exclude, extends defaults
--exclude.glob    repeatable glob exclude for watched events
-v                verbose Gust logs
```

Semantics:

- Missing `-e` is an error and prints usage.
- Missing `-p` is allowed. Gust then works as a generic rerunner.
- `-p 8080` means app port only, no proxy.
- `-p 8080:5000` means app port `8080`, proxy port `5000`.
- Proxy mode is enabled only with `app:proxy` port syntax.
- `-h` is allowed without proxy, but requires app port.
- V1 has no config file.

## Child process

- Gust runs the command immediately on startup.
- Command execution uses:

```sh
/bin/sh -c '<cmd>'
```

- Child inherits Gust environment.
- Gust also sets:

```text
GUST=1
GUST_APP_PORT=<app_port if set>
GUST_PROXY_PORT=<proxy_port if set>
```

- Gust does not set `PORT` automatically.
- Child stdout/stderr are streamed raw.
- Gust logs are prefixed with `[gust]`.

On rerun:

1. Send `SIGTERM` to the child process group.
2. Wait `2s`.
3. Send `SIGKILL` if still alive.
4. Wait until the process is reaped.
5. Start the new command.

If the old process cannot be stopped, Gust logs an error and does not start a duplicate.

If the child exits by itself:

- Gust logs the exit.
- Gust does not auto-restart.
- Gust waits for next FS/manual/agent trigger.
- Browser clients get an error banner because the app is no longer available.

## File watching

- V1 uses `fsnotify` on Linux/inotify.
- Root is the current working directory.
- Gust watches recursively by walking directories and adding watchers.
- Newly-created directories are added to the watcher.
- Symlinked directories are not followed.
- Polling is out of scope.

Default ignored directories:

```text
.git
node_modules
vendor
tmp
dist
build
.cache
```

User excludes:

- `--exclude <path>` is repeatable.
- User excludes extend defaults.
- User excludes are cleaned path prefixes relative to root.
- `--exclude.glob <glob>` is repeatable.
- Glob excludes use Go filepath glob syntax.
- Glob excludes are matched against both project-relative paths and basenames.

Triggering events:

```text
write
create
remove
rename
```

Ignored events:

```text
chmod
```

Errors:

- If root cannot be watched, Gust exits.
- If a subdirectory cannot be watched, Gust warns and continues.

## Trigger behavior

Trigger sources:

- filesystem changes
- keyboard `r`
- agent socket `rerun`
- initial startup

Rules:

- FS triggers are debounced by `500ms`.
- Manual and agent triggers have higher priority.
- Manual/agent trigger during FS debounce cancels the pending FS trigger and runs immediately.
- During an active restart, new triggers are coalesced into one pending rerun.
- If a trigger arrives during readiness/reload wait, stale readiness/reload is canceled and the pending rerun starts.

## Readiness

If `-h` is set:

- Gust polls:

```text
http://127.0.0.1:<app_port><health_path>
```

- Any `2xx` response means ready.
- Timeout: `10s`.
- Interval: `200ms`.
- If timeout expires, log error and do not reload browser.
- If process exits before health succeeds, stop polling and do not reload.

If `-h` is not set:

- Process is considered ready after it stays alive for `300ms`.

Browser version increments only after readiness succeeds.

## Terminal controls

- If stdin is a TTY, Gust enters raw mode.
- `r` reruns immediately.
- `q` quits.
- Ctrl-C quits.
- Terminal state is restored on exit.
- If stdin is not a TTY, keyboard controls are disabled and Gust logs that. Socket controls still work.

## Agent socket

Gust exposes a Unix domain socket for local agent control.

Path:

```text
/tmp/gust-<uid>/<hash>.sock
```

- `<hash>` is deterministic from absolute cwd.
- Socket directory is user-owned and `0700`.
- Gust prints socket path on startup.
- Gust removes stale socket file on startup.
- No auth in v1.

Protocol:

- One JSON request per connection.
- One JSON response.
- Connection closes after response.

Supported requests:

```json
{"action":"rerun"}
{"action":"status"}
```

Rerun response after coordinator accepts the trigger:

```json
{"ok":true,"status":"queued"}
```

Shutdown response when possible:

```json
{"ok":false,"error":"shutting_down"}
```

Status response shape:

```json
{
  "ok": true,
  "state": "running|restarting|stopped|shutting_down",
  "pid": 123,
  "app_port": 8080,
  "proxy_port": 5000,
  "version": 12
}
```

## Proxy

Enabled only with:

```sh
-p <app_port>:<proxy_port>
```

Behavior:

- Proxy binds `127.0.0.1:<proxy_port>`.
- Proxy forwards to `http://127.0.0.1:<app_port>`.
- If proxy port is in use, Gust exits.
- Gust does not pre-check app port.
- During restart or app unavailability, proxy retries for up to `10s` every `200ms`, then returns `502`.
- Proxy supports request bodies.
- Proxy streams non-HTML responses.
- Proxy reserves all `/__gust/*` paths.
- `/__gust/ws` is Gust's WebSocket endpoint.
- Other WebSocket upgrades are proxied to the app.

## HTML injection

Gust injects browser code only when response `Content-Type` contains `text/html`.

Injection rules:

- Preserve original `Content-Type` exactly.
- Do not inject into `application/xhtml+xml`.
- Inject into any HTML status code, including error pages.
- Append script at EOF. No `</body>` parsing in v1.
- Buffer eligible HTML responses.
- If `Content-Length` was present, update it.
- If absent, leave it absent.
- Remove `ETag`.
- Preserve `Last-Modified`.
- Set `Cache-Control: no-store`.
- Do not inject into `HEAD` responses.
- Skip request `Range` and response `206`.
- Skip `Content-Disposition: attachment`.
- Remove `Accept-Encoding` from all proxied requests.
- If response has `Content-Encoding` other than empty/`identity`, skip injection.
- No decompression/recompression in v1.
- Check marker `__gust_reload` to avoid double injection.
- V1 has no max HTML size limit.

Injected script:

- Has marker:

```html
<script id="__gust_reload">...</script>
```

- Connects to:

```js
const proto = location.protocol === "https:" ? "wss:" : "ws:";
const url = proto + "//" + location.host + "/__gust/ws";
```

- Includes current version:

```js
let lastVersion = <currentVersion>;
```

- Reloads with:

```js
location.reload();
```

- Tracks `lastVersion` in memory only.
- Reloads only when incoming reload version is newer.
- Reconnects WebSocket with backoff.
- Does not reload merely because WebSocket disconnected.

WebSocket messages:

```json
{"type":"reload","version":12}
{"type":"ready","version":12}
{"type":"error","message":"health check timed out"}
```

- On connect, server sends latest `ready` or `error`, not `reload`.
- `error` shows banner.
- `ready` hides banner.
- `reload` hides banner and reloads if version is newer.

Browser banner:

- Simple fixed top overlay.
- Minimal inline CSS.
- High z-index.
- Text only.
- Not dismissible in v1.
- No Shadow DOM.
- No log streaming.

Banner is shown for:

- command cannot start
- command exits non-zero or too early
- health check timeout
- app exits before health
- proxy cannot reach app after retry timeout

## Coordinator and state machine

Gust uses one central coordinator/event loop that owns runtime state.

Event producers:

- watcher
- keyboard input
- socket server
- process wait goroutine
- restart worker
- shutdown signals

Coordinator owns:

- process state
- process handle
- pending rerun
- readiness cancellation
- browser version
- current browser error/status

Internal states:

```text
stopped
starting
waiting_ready
running
stopping
shutting_down
```

External status may expose `restarting` for stop/start/waiting-ready phases.

Important events:

```text
Trigger{source, reason}
DebouncedFSTrigger
ProcessExited{runID,pid,code,err}
RestartComplete{runID, ready bool, error string}
StatusRequest{reply chan}
ShutdownRequested
ForceShutdown
```

Run/version rules:

- `runID` increments for every process start attempt.
- Browser `version` increments only after readiness succeeds.
- Stale events are ignored by `runID`.

Restart work:

- Runs in a worker goroutine.
- At most one restart worker runs at a time.
- Coordinator stays responsive while restart work runs.
- New triggers during restart set `pendingRerun=true`.

Shutdown:

1. Set state to `shutting_down`.
2. Stop accepting socket requests.
3. Stop watcher.
4. Cancel debounce/readiness/restart.
5. Stop child process group.
6. Close proxy and WebSocket clients.
7. Restore terminal.
8. Remove socket.
9. Exit.

Shutdown wins over restart.

## Logging and startup output

Gust logs are prefixed:

```text
[gust] ...
```

App logs are raw.

Startup prints applicable lines:

```text
[gust] exec: go run ./cmd/server
[gust] app: http://127.0.0.1:8080
[gust] proxy: http://127.0.0.1:5000
[gust] health: http://127.0.0.1:8080/health
[gust] socket: /tmp/gust-1000/<hash>.sock
[gust] keys: r=rerun, q=quit
```

Verbose `-v` logs include watcher events, skipped injection, socket requests, health retries, and similar diagnostics.

## Implementation architecture

Gust is implemented in Go.

Package layout:

```text
cmd/gust/main.go
internal/config
internal/cli
internal/coordinator
internal/process
internal/watcher
internal/proxy
internal/socket
internal/term
internal/logger
```

`cmd/gust/main.go`:

1. parse/validate CLI
2. create logger
3. create root context/signal handling
4. construct coordinator
5. run coordinator
6. print fatal errors and exit non-zero

`internal/config` owns `Config` and fixed constants.

```go
type Config struct {
    Exec string
    AppPort int
    HasAppPort bool
    ProxyPort int
    ProxyEnabled bool
    HealthPath string
    Excludes []string
    Verbose bool
    Root string
}
```

Ports are `int` after validation in range `1..65535`.

Fixed constants:

```text
FS debounce: 500ms
health timeout: 10s
health interval: 200ms
stability window: 300ms
shutdown timeout: 2s
proxy retry timeout: 10s
proxy retry interval: 200ms
```

Dependencies:

- `github.com/fsnotify/fsnotify`
- `github.com/coder/websocket`

CLI uses stdlib `flag`.

## Test plan

Use unit tests for parsing and state behavior. Use integration tests for process, watcher, proxy, socket, and Linux OS behavior.

Must cover:

- CLI parsing and validation.
- Watch exclude behavior and fs events.
- Debounce and trigger priority.
- Restart coalescing and stale `runID` handling.
- Process group stop/reap behavior.
- Health readiness and timeout behavior.
- Proxy forwarding, retry, reserved paths, and WebSocket handling.
- HTML injection rules.
- Browser message behavior.
- Agent socket rerun/status/shutdown responses.
- Terminal raw key behavior where practical.
- Logging prefix and verbose mode.

Test assumptions:

- Linux is required for v1 CI/runtime.
- Use temp dirs and random free ports.
- Avoid privileged ports and external network.
- Raw terminal behavior can use manual checks or PTY tests.

## V1 non-goals

Out of scope for v1:

- Config files.
- Cross-platform support beyond Linux.
- Polling watcher.
- Include filters / extension filters / explicit watch paths.
- Separate build and run pipeline.
- Pre/post hooks.
- `.env` loading.
- Custom health timeout or interval.
- Custom shutdown timeout.
- Custom bind/forward hosts.
- TLS/HTTPS proxy.
- Multiple apps or ports.
- Task/codegen rules.
- Keeping old process alive when new command fails.
- Browser log streaming.
- HMR, CSS-only reload, framework adapters.
- Proxy/socket auth and CORS controls.
- Advanced terminal UI.
- Generated binary/tmp management.
- Root/workdir customization.
- Color configuration.
