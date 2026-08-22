# Gust v1 Design Q&A

## CLI UX and config

- V1 is flag-only. No config file until v2.
- `-e <cmd>` is required and runs through `/bin/sh -c '<cmd>'`.
- Gust runs the command immediately on startup.
- `-p` is optional:
  - `-p 8080` means app port only, no proxy.
  - `-p 8080:5000` means app port 8080 and proxy port 5000.
- `-h <path>` is optional and requires an app port.
- `--exclude <path>` is repeatable and extends default watch excludes.
- `-v` enables verbose logging.
- Missing `-e` is an error and prints usage.
- Missing `-p` is allowed; Gust still works as a generic rerunner.
- Child process inherits Gust environment.
- Gust sets:
  - `GUST=1`
  - `GUST_APP_PORT=<app_port if set>`
  - `GUST_PROXY_PORT=<proxy_port if set>`
- Gust does not set `PORT` automatically.

## Process lifecycle and triggers

- If the child command exits, Gust logs it and waits for the next trigger. No automatic restart loop.
- On rerun, Gust sends `SIGTERM` to the child process group, waits `2s`, then sends `SIGKILL` if needed.
- Gust waits until the old process is reaped before starting a new one.
- If the old process cannot be stopped, Gust logs an error and avoids starting a duplicate process.
- FS triggers are debounced by `500ms`.
- Manual and agent triggers are higher priority.
- A manual/agent trigger during FS debounce runs immediately and consumes the pending FS trigger.
- During an active restart, new triggers are coalesced into one pending rerun.
- If a new trigger arrives during readiness/reload wait, Gust cancels stale readiness/reload and proceeds to the pending rerun.
- Any successful rerun source increments browser version and reloads when proxy is enabled.
- Without health check, a successful rerun means the process spawned and stayed alive for `300ms`.
- With health check, if the process exits before health succeeds, Gust stops polling and does not reload.

## File watching

- Gust watches the current working directory recursively.
- Default ignored directories:
  - `.git`
  - `node_modules`
  - `vendor`
  - `tmp`
  - `dist`
  - `build`
  - `.cache`
- V1 uses `fsnotify` on Linux/inotify.
- Recursive watching is implemented by walking directories and adding watchers.
- Newly-created directories are added to the watcher.
- Gust does not follow symlinked directories.
- Triggering FS events:
  - write
  - create
  - remove
  - rename
- Ignored FS events:
  - chmod
- No checksum filtering in v1.
- If root cannot be watched, Gust exits.
- If a subdirectory cannot be watched, Gust warns and continues.

## Manual and agent controls

- Terminal runs in raw mode.
- Press `r` to rerun immediately.
- Press `q` to quit.
- Ctrl-C quits.
- Terminal state is restored on exit.
- V1 exposes a Unix domain socket for local agent control.
- Socket path is deterministic from absolute cwd hash.
- Socket lives under a user-owned `0700` directory, e.g. `/tmp/gust-<uid>/<hash>.sock`.
- Gust prints the socket path on startup.
- Gust removes stale socket file on startup.
- No socket auth in v1.
- Protocol: one JSON request per connection, one JSON response, then close.
- Supported actions:
  - `{"action":"rerun"}`
  - `{"action":"status"}`
- `rerun` returns immediately after accepting/queuing trigger:
  - `{"ok":true,"status":"queued"}`
- During shutdown, socket requests return if possible:
  - `{"ok":false,"error":"shutting_down"}`
- Example `status` response:

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

## Proxy and browser reload

- Proxy is enabled only with `-p <app_port>:<proxy_port>`.
- Proxy binds to `127.0.0.1` only.
- Proxy forwards to `http://127.0.0.1:<app_port>`.
- If proxy port is already in use, Gust exits with error.
- Gust does not pre-check app port.
- Health checks hit app port directly:
  - `http://127.0.0.1:<app_port><health_path>`
- Health ready means any `2xx` response.
- Health retry behavior is fixed in v1:
  - timeout `10s`
  - interval `200ms`
- On health timeout, Gust logs failure and does not reload.
- `-h` is allowed without proxy and reports readiness in terminal.
- Browser reload only applies when proxy is enabled.
- With `-h`, browser reload happens after health succeeds.
- Without `-h`, browser reload happens after the child process stays alive for `300ms`.
- If command fails to start, no reload.
- Proxy injects reload script into responses with `Content-Type` containing `text/html`.
- Preserve the original `Content-Type` exactly, including charset parameters.
- Do not inject into `application/xhtml+xml`.
- Injection applies to any HTML status code, including error pages.
- Injection appends the script at EOF. No `</body>` parsing in v1.
- Eligible HTML responses are buffered before injection.
- If `Content-Length` was present, update it to the new byte length after injection.
- If `Content-Length` was absent, leave it absent.
- For injected HTML responses:
  - remove `ETag`
  - leave `Last-Modified`
  - set `Cache-Control: no-store`
- Do not inject into `HEAD` responses.
- Skip injection when request has `Range` or response status is `206 Partial Content`.
- Skip injection when `Content-Disposition` contains `attachment`.
- Proxy removes `Accept-Encoding` from all proxied requests.
- If app still returns a compressed response, Gust skips injection.
  - Compressed means `Content-Encoding` is present and not empty/`identity`.
- No decompression/recompression in v1.
- Injected script has marker/id:
  - `<script id="__gust_reload">...</script>`
- Before injecting, Gust checks for `__gust_reload` in the HTML body and skips if already present.
- Proxy reserves all `/__gust/*` paths.
- `/__gust/*` paths are internal and are never proxied or injected.
- Injected script connects to WebSocket endpoint:
  - `/__gust/ws`
- Script builds the WebSocket URL from the current page:

```js
const proto = location.protocol === "https:" ? "wss:" : "ws:";
const url = proto + "//" + location.host + "/__gust/ws";
```

- Injected script includes current server version:

```js
let lastVersion = <currentVersion>;
```

- Browser reload uses:

```js
location.reload();
```

- No cache-busting query parameter in v1.
- Server sends latest `ready`/`error` on WebSocket connect, not `reload`.
- Browser tracks `lastVersion` in memory only.
- Browser reloads only when `reload.version > lastVersion`.
- Browser reconnects WebSocket with backoff and does not reload merely because socket disconnected.
- Browser UI is limited to a simple fixed top error banner.
- The banner is a fixed top overlay and does not push page content down.
- The banner is not dismissible in v1.
- The banner uses unique ids and inline styles. No Shadow DOM.
- The banner has minimal CSS, high z-index, and text only.
- The banner shows app lifecycle problems:
  - command cannot start
  - command exits non-zero or too early
  - health check timeout
  - app exits before health
  - proxy cannot reach app after retry timeout
- The banner hides on next successful ready/reload.
- Browser WebSocket messages:

```json
{"type":"reload","version":12}
{"type":"error","message":"health check timed out"}
{"type":"ready","version":12}
```

- On WebSocket connect, Gust sends latest state immediately.
- No localStorage for browser state.
- App stdout/stderr are not sent to browser.
- Injected JS should be readable in source. No minification requirement.
- V1 buffers all eligible HTML responses. No max-size limit.
- Proxy supports request bodies.
- Proxy streams non-HTML responses.
- Proxy buffers only eligible HTML responses for injection.
- For normal streamed responses, preserve reverse-proxy trailer behavior.
- For injected buffered HTML, trailers may be dropped.
- Proxy reserves `/__gust/ws` for Gust and proxies other WebSocket upgrades to the app.
- If app is unavailable, proxy waits/retries up to `10s` with `200ms` interval, then returns `502`.
- During restart, proxy holds/retries requests up to `10s`, then returns `502`.

## Logging and terminal UX

- Gust logs are prefixed with `[gust]`.
- App stdout/stderr are streamed raw.
- Gust never clears the screen automatically in v1.
- Startup info includes:
  - exec command
  - app URL if set
  - proxy URL if enabled
  - health URL if set
  - socket path
  - key controls
- `-v` enables verbose logging for watcher events, skipped injection, socket requests, health retries, etc.

## Failure handling

- If command fails or exits immediately, Gust logs it and keeps watching.
- No automatic retry.
- User can fix files or press `r` / use agent rerun.

## State machine / concurrency model

- Gust uses one central coordinator/event loop that owns process state.
- Event producers send events into the coordinator:
  - FS watcher
  - keyboard input
  - agent socket
  - process exit monitor
  - readiness checker / restart worker
  - shutdown signals
- Coordinator owns:
  - process state
  - pending rerun
  - readiness cancellation
  - browser version
  - current browser error/status
- Internal states:
  - `stopped`
  - `starting`
  - `waiting_ready`
  - `running`
  - `stopping`
  - `shutting_down`
- External/socket status may expose `restarting` as an alias for stop/start/waiting-ready phases.
- Coordinator events:
  - `Trigger{source: fs|keyboard|socket, reason}`
  - `DebouncedFSTrigger`
  - `ProcessExited{runID,pid,code,err}`
  - `RestartComplete{runID, ready bool, error string}`
  - `StatusRequest{reply chan}`
  - `ShutdownRequested`
  - `ForceShutdown`
- Use `runID` so stale process/readiness events cannot affect newer runs.
- `runID` increments for every process start attempt.
- Browser `version` increments only after readiness succeeds.
- Restart algorithm:

```text
trigger received
cancel FS debounce if applicable
cancel readiness wait if active

if process running:
  state=stopping
  stop process group
  wait until reaped

state=starting
start command
increment runID

if start fails:
  state=stopped
  currentError=start error
  notify browser error
  wait for next trigger

state=waiting_ready
wait for health or 300ms stability window

if ready and no pending rerun:
  version++
  state=running
  clear currentError
  notify browser ready/reload

if new trigger arrives during stop/start/waiting_ready:
  pendingRerun=true
  cancel readiness if active
  finish safe current action
  loop into another restart before notifying reload
```

- Restart work runs in a worker goroutine.
- Coordinator remains responsive while restart work is happening.
- At most one restart worker runs at a time.
- New triggers during restart set `pendingRerun=true`.
- Coordinator owns process handle state.
- A process manager object performs start/stop operations.
- Blocking stop/reap work happens in the restart worker.
- Process manager is called only under coordinator authority.
- Each successful process start gets a Wait goroutine that sends:
  - `ProcessExited{runID,pid,code,err}`
- Coordinator ignores stale `runID`s.
- If process exits while running:
  - state becomes `stopped`
  - no auto-rerun
  - browser gets an error/banner because app is no longer available
  - zero exit message: `process exited`
  - non-zero exit message includes exit failure
- If process exits during `waiting_ready`:
  - coordinator treats `ProcessExited` as authoritative
  - readiness is canceled
  - state becomes `stopped`
  - current error is `app exited before ready`
  - stale readiness results are ignored by `runID`
- Readiness wait lives inside the restart worker and is cancellable.
- Restart worker reports:
  - `RestartComplete{runID, ready bool, error string}`
- Process exits remain separate from restart completion.
- Shutdown algorithm:

```text
ShutdownRequested
state=shutting_down
stop accepting socket requests
stop watcher
cancel debounce/readiness/restart
stop child process group
close proxy/ws
restore terminal
remove socket
exit
```

- Shutdown uses the same fixed `2s` SIGTERM → SIGKILL process shutdown.
- Shutdown wins over restart.
- Coordinator cancels restart context and performs final cleanup.
- FS debounce timer is owned by coordinator.
- Watcher sends raw accepted FS events; coordinator resets the 500ms timer.
- Manual/socket trigger during FS debounce:
  - stops debounce timer
  - clears `fsPending`
  - immediately starts or queues restart
- Socket `status` uses coordinator request/reply:
  - `StatusRequest{reply chan}`
- Socket `rerun` sends a trigger with ack channel.
- Coordinator acks accepted/rejected, then socket returns JSON.
- If stdin is not a TTY, raw keyboard controls are disabled and Gust logs that.
- Socket controls still work when stdin is not a TTY.
- No special self-write tracking.
- Gust should not write project files during runtime.
- Coordinator notifies proxy on state changes:
  - `proxy.NotifyReady(version)`
  - `proxy.NotifyReload(version)`
  - `proxy.NotifyError(message)`
  - `proxy.SetRestarting(true/false)`
- Proxy owns:
  - WebSocket clients
  - latest browser state
  - request holding/retry behavior
- `ready` is for status-only readiness.
- `reload` is for browser reload.
- Both `ready` and `reload` clear browser banner.
- Do not send both for the same state transition unless later needed.
- Initial startup sends `reload` if proxy has connected clients.
- New pages receive current version in injected script and do not reload-loop.
- Coordinator tracks trigger source/reason and logs rerun reason.
- For FS debounce with many files, log first path plus count.
- Pending rerun keeps aggregate summary:
  - source counts
  - last reason
- Process manager streams child stdout/stderr directly to terminal via copy goroutines.
- Coordinator only logs Gust events.

## Implementation architecture / package layout

- Gust is implemented in Go.
- Initial package layout:

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

- `cmd/gust/main.go` is thin:
  1. parse/validate CLI
  2. create logger
  3. create root context/signal handling
  4. construct coordinator with config/logger
  5. run coordinator
  6. print fatal errors and exit non-zero
- `internal/config` owns:
  - `Config`
  - fixed v1 constants
- Runtime config shape:

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

- Ports use `int` internally after validation in range `1..65535`.
- Fixed v1 durations are constants, not CLI config fields:
  - FS debounce: `500ms`
  - health timeout: `10s`
  - health interval: `200ms`
  - stability window: `300ms`
  - shutdown timeout: `2s`
  - proxy retry timeout: `10s`
  - proxy retry interval: `200ms`
- `internal/cli` parses and validates flags.
- `internal/cli` returns `config.Config`.
- Coordinator creates and owns runtime subsystems:
  - process manager
  - watcher
  - proxy if enabled
  - socket server
  - terminal input
- Main only passes config/logger/context.
- No `internal/events` package initially.
- Event types live in `internal/coordinator`.
- Subsystems expose narrow callbacks.
- Coordinator adapts callbacks into internal events.
- `internal/process` handles:
  - `/bin/sh -c`
  - process group setup
  - stdout/stderr streaming
  - wait support
  - SIGTERM/SIGKILL group shutdown
- Candidate process API:

```go
type Manager struct {}

func (m *Manager) Start(ctx context.Context, cmd string, env []string) (*Process, error)
func (m *Manager) Stop(ctx context.Context, p *Process, timeout time.Duration) error
```

- Coordinator builds child env from `os.Environ()` plus `GUST_*`.
- Process package does not know config semantics.
- `internal/watcher` owns:
  - fsnotify setup
  - recursive directory walking
  - exclude filtering
  - adding newly-created dirs
  - accepted FS event filtering
- Candidate watcher API:

```go
type Event struct {
    Path string
    Op   Op
}

type Watcher struct {}

func New(root string, excludes []string, log *logger.Logger) (*Watcher, error)
func (w *Watcher) Run(ctx context.Context, onChange func(Event)) error
```

- Default excludes are directory names matched anywhere.
- User `--exclude` values are cleaned path prefixes relative to root.
- No globbing in v1.
- `internal/proxy` owns:
  - HTTP server
  - reverse proxy
  - HTML injection
  - WebSocket clients
  - latest browser state
  - request retry/holding
- Candidate proxy API:

```go
type Proxy struct {}

func New(appPort, proxyPort int, log *logger.Logger) *Proxy
func (p *Proxy) Run(ctx context.Context) error
func (p *Proxy) NotifyReady(version int)
func (p *Proxy) NotifyReload(version int)
func (p *Proxy) NotifyError(message string)
func (p *Proxy) SetRestarting(bool)
func (p *Proxy) HasClients() bool
```

- Proxy does not perform health checks.
- Coordinator/restart worker handles readiness.
- `internal/socket` owns:
  - deterministic socket path helper
  - Unix socket listener
  - JSON request/response protocol
  - stale socket cleanup
  - socket file cleanup
- Candidate socket API:

```go
type Server struct {}
type Status struct {...}

func SocketPath(root string) string
func New(path string, handlers Handlers, log *logger.Logger) *Server
func (s *Server) Run(ctx context.Context) error

type Handlers struct {
    Rerun  func(context.Context) Ack
    Status func(context.Context) Status
}
```

- Coordinator supplies socket handlers.
- `internal/term` owns:
  - TTY detection
  - raw mode
  - key reading
  - terminal restore
- Candidate term API:

```go
func RunKeys(ctx context.Context, onRerun func(), onQuit func()) error
```

- If stdin is not a TTY, term no-ops cleanly and logs keyboard controls unavailable.
- `internal/logger` provides a thin logger:
  - `Info(...)`
  - `Verbose(...)`
  - `Warn(...)`
  - `Error(...)`
- Logger prefixes Gust logs with `[gust]`.
- Logger gates verbose logs behind `-v`.
- Logger does not touch app stdout/stderr.
- Dependencies:
  - `github.com/fsnotify/fsnotify`
  - `github.com/coder/websocket`
- CLI parsing uses stdlib `flag`.
- Error propagation:
  - CLI parse/validation error: fatal
  - root watcher init error: fatal
  - proxy listen error: fatal
  - socket listen error: fatal
  - runtime watcher subdir errors: warn
  - runtime proxy request errors: per-request response/log verbose

## Test plan

- Prefer unit tests for pure parsing/state behavior and integration tests for OS/process/proxy behavior.
- CLI tests:
  - missing `-e` fails with usage
  - missing `-p` is allowed
  - `-p 8080` parses app port only
  - `-p 8080:5000` enables proxy
  - invalid ports fail
  - `-h` without app port fails
  - repeated `--exclude` extends defaults
  - `-v` enables verbose mode
- Watcher tests:
  - default excludes are skipped
  - user excludes are cleaned relative prefixes
  - write/create/remove/rename trigger events
  - chmod does not trigger
  - newly-created directories are watched
  - symlinked directories are not followed
  - inaccessible subdirs warn/continue where practical
- Coordinator/state tests:
  - startup immediately triggers first run
  - FS events debounce for 500ms
  - manual/socket trigger during FS debounce cancels pending FS trigger
  - triggers during restart coalesce into one pending rerun
  - pending rerun cancels stale readiness/reload
  - stale `runID` events are ignored
  - browser `version` increments only after readiness succeeds
  - process exit while running moves state to `stopped`
  - shutdown wins over restart
  - socket status receives authoritative coordinator snapshot
- Process tests:
  - command runs through `/bin/sh -c`
  - child receives `GUST`, `GUST_APP_PORT`, `GUST_PROXY_PORT`
  - child stdout/stderr stream raw
  - stop sends SIGTERM to process group, then SIGKILL after timeout
  - child process is reaped before next start
  - no auto-rerun after child exit
- Health/readiness tests:
  - without health, ready after process survives 300ms
  - with health, any 2xx is ready
  - health polls app port directly
  - health timeout reports error and does not reload
  - process exit before health reports error and cancels polling
- Proxy tests:
  - proxy binds `127.0.0.1:<proxy_port>`
  - proxy forwards to `127.0.0.1:<app_port>`
  - proxy port already in use is fatal
  - app unavailable causes retry up to fixed timeout, then 502
  - non-HTML responses stream without injection
  - request bodies are proxied
  - `/__gust/*` is reserved and not proxied
  - `/__gust/ws` serves Gust WebSocket
  - other WebSocket upgrades proxy to the app
- Injection tests:
  - inject only when `Content-Type` contains `text/html`
  - do not inject into `application/xhtml+xml`
  - append script at EOF
  - update `Content-Length` when present
  - remove `ETag`, preserve `Last-Modified`, set `Cache-Control: no-store`
  - skip `HEAD`
  - skip request `Range` and response `206`
  - skip `Content-Disposition: attachment`
  - remove `Accept-Encoding` from proxied requests
  - skip compressed responses using `Content-Encoding`
  - do not inject twice when `__gust_reload` marker exists
  - injected script includes current version
- Browser/WebSocket tests:
  - WS connect receives latest `ready` or `error`, not `reload`
  - `error` shows banner
  - `ready` hides banner
  - `reload` hides banner and calls `location.reload()` only for newer version
  - reconnect uses backoff
  - banner is fixed top overlay and not dismissible
- Socket tests:
  - socket path is deterministic from root hash
  - socket dir is user-owned `0700`
  - stale socket cleanup happens on startup
  - `rerun` returns queued after coordinator ack
  - `status` returns current state/pid/ports/version
  - shutdown returns `{"ok":false,"error":"shutting_down"}` when possible
- Terminal tests/manual checks:
  - raw `r` triggers rerun
  - raw `q` quits
  - Ctrl-C quits
  - terminal state is restored on exit
  - non-TTY disables keyboard controls without failing
- Logging tests/manual checks:
  - Gust logs are prefixed `[gust]`
  - app logs are raw
  - startup info includes exec/app/proxy/health/socket/keys as applicable
  - verbose logs only appear with `-v`
- Test environment:
  - Linux is the required CI/runtime target for v1.
  - Use temporary directories and random free ports for integration tests.
  - Avoid tests that require privileged ports or external network access.
  - Some terminal/raw-mode behavior can be covered by focused manual tests if PTY automation is too costly for v1.

## V1 non-goals / deferred v2 items

- Config files are out of scope for v1.
  - No `gust.toml`.
  - No `.gust.toml`.
  - No project config discovery.
- V1 is Linux-only.
  - Avoid unnecessary portability blockers where simple.
- Polling file watcher is out of scope.
  - `fsnotify` only.
- Include filters are out of scope.
  - No extension filters.
  - No explicit watch paths.
  - Gust watches all regular files except excludes.
- Separate build/run pipeline is out of scope.
  - `-e` is the whole process command.
- Pre/post hooks are out of scope.
  - Users can compose shell commands in `-e`.
- `.env` loading is out of scope.
  - Users can source env files inside `-e` if needed.
- Custom health timeout/retry flags are out of scope.
  - Fixed timeout: `10s`.
  - Fixed interval: `200ms`.
- Custom graceful shutdown timeout is out of scope.
  - Fixed timeout: `2s`.
- Custom bind/forward hosts are out of scope.
  - V1 uses `127.0.0.1`.
- TLS/HTTPS proxy is out of scope.
  - HTTP-only proxy.
- Multiple apps/ports are out of scope.
  - One command.
  - One optional app port.
  - One optional proxy port.
- Task/codegen rules are out of scope.
  - Examples: `templ generate`, `sqlc generate`, `go generate` rules.
  - Users can compose in `-e`; richer rules belong in v2 config.
- Keeping old process alive on failed restart is out of scope.
  - V1 uses stop-then-start.
- Browser log streaming is out of scope.
  - Browser shows concise Gust-generated status/error only.
  - Full logs stay in terminal.
- HMR, CSS-only reload, and framework adapters are out of scope.
  - Full page reload only.
- Proxy/socket auth and CORS controls are out of scope.
  - Security model is local-only/same-user.
- Advanced terminal UI is out of scope.
  - No dashboard, spinner requirement, TUI, or status table.
  - Plain logs plus `r`/`q` keys only.
- Generated binary/tmp management is out of scope.
  - Gust creates no project tmp dirs and manages no build artifacts.
- Root directory selection is out of scope.
  - Root is current working directory.
- Command working directory customization is out of scope.
  - Child runs in Gust current working directory.
- Color configuration is out of scope.
  - Logs should be readable without relying on colors.
