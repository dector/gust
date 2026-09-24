# Gust

Gust is a Linux-first local development runner inspired by Air.

It runs one command, watches the current directory, restarts the command on file changes, and exposes a local Unix socket for agent control. It can also run an HTTP proxy that injects a small browser reload script.

## Install

Latest trunk snapshot with mise:

```sh
mise use -g ubi:dector/gust@snapshot
```

The `snapshot` release is rebuilt on every push to `trunk`.

As a Go tool:

```sh
go get -tool github.com/dector/gust@latest
go tool gust -e 'go run ./cmd/server'
```

## Usage

```sh
gust -e 'go run ./cmd/server'
gust -e 'go run ./cmd/server' --e.before 'templ generate' --e.before 'sqlc generate'
gust -e 'go run ./cmd/server' --e.after 'notify-send reloaded'
gust -e 'go run ./cmd/server' -p 8080
gust -e 'go run ./cmd/server' -p 8080:5000
gust -e 'go run ./cmd/server' -p 8080:5000 -h /health
gust -e 'go run ./cmd/server' -p 8080 -h /health -T
gust -e 'go run ./cmd/server' -p 8080:5000 -h /health -T
gust -e 'go run ./cmd/server' -p '?:?' -h /health -T
gust -e 'go run ./cmd/server' -p 8080 --exclude frontend/node_modules -v
gust -e 'go run ./cmd/server' --exclude.glob '*_templ.go'
gust -e 'go run ./cmd/server' -p 8080:5000 --optin comments
```

Flags:

- `-e <cmd>`: required command. Gust runs it with `/bin/sh -c`.
- `--e.before <cmd>`: repeatable command to run before each rerun. Fail-fast; a
  failure aborts the rerun and leaves the running app untouched.
- `--e.after <cmd>`: repeatable command to run after each start.
- `-p <port>`: optional app port, or `app:proxy` ports. `?` asks the OS for a free port (for example `-p '?'`, `-p '?:?'`, `-p '8080:?'`). `?` starts at a path-derived port and probes for a free one, so ports stay the same across launches when available. An empty proxy slot (`-p '?:'`) disables the proxy; an empty app slot cannot be proxied because Gust cannot discover the server's default port. Without `-p`, the app chooses its own port. Random ports are passed to the app as `GUST_APP_PORT` / `GUST_PROXY_PORT`; the app must listen on `GUST_APP_PORT`. There is a small race between choosing ports and the app/proxy binding them.
- `-h <path>`: optional health endpoint. Requires an app port.
- `-T`: expose the reload-proxy port through Tailscale Serve when enabled; otherwise expose the app port. Requires `-p`; no argument.
- `--exclude <path>`: repeatable watched-path exclude.
- `--exclude.glob <glob>`: repeatable glob exclude for watched events.
- `--optin <feature>`: opt in to an optional feature; repeatable. `comments` enables browser comments and `gust ctl comments` (disabled by default).
- `--self-dev`: with `--optin comments` and a proxy, let Ctrl+click select Gust panel elements in comment mode; reload the page after the proxy process reconnects. Works with `-T`. Default behavior still excludes Gust's UI.
- `-v`: verbose Gust logs.
- `GUST_INFO=1`: enable info logs initially. Press `i` to toggle them.

`--exclude` values are relative path prefixes. Use them for directories or whole subtrees, for example `--exclude frontend/node_modules`.

`--exclude.glob` values use Go filepath glob syntax and are matched against both the project-relative path and the file basename. The pattern must match the whole value. `*` does not cross `/`, so `assets/*.tmp` matches `assets/cache.tmp` but not `assets/nested/cache.tmp`. A basename glob like `*_templ.go` matches files with that name pattern in any directory.

## Developing Gust's own panel

Run `ror dev:self` from the repository root. The external supervisor in
`tools/dev-self.sh` builds Gust before restarting it on source changes; if a
build fails, the running instance stays up. It serves `docs/demo` on
path-stable app and proxy ports (when available) and enables Tailscale Serve (`-T`). Gust's
keyboard controls (`r`, `s`, `i`, `D`) work through the wrapper. Press `q` or
Ctrl+C to restart Gust; press Ctrl+C again to stop the wrapper. To disable
Tailscale locally, use `DEV_SELF_TAILSCALE=0 ror dev:self`.

In comment mode, Ctrl+click a **panel** element to select it; normal clicks
still operate the panel. Logs and existing comments are not selectable. Gust's
icon and floating editor remain unselectable. `-T` exposes this dev mode to
anyone allowed to access the Tailscale Serve URL; use it only with trusted viewers.
After the supervisor restarts Gust, the browser reloads on reconnection.
Comments are still in memory: have the agent read or record submitted comments
*before* editing Gust source, because a successful rebuild restarts Gust.

## Tailscale exposure

Install and sign in to Tailscale, then use `-T -p <app port>` or
`-T -p <app port>:<proxy port>`. Gust starts a foreground `tailscale serve`
when startup reaches the proxy setup, before app readiness checks. It targets
`127.0.0.1:<proxy port>` when the reload proxy is enabled, including its
WebSocket traffic; otherwise it targets `127.0.0.1:<app port>`. Gust logs
the HTTPS URL when Serve reports it and warns if exposure fails or exits. An
exposure failure does not stop the local app; a later successful app restart
retries it.

The HTTPS port is deterministic for the project directory and exposed port, in the
high range 49152–65535. If it is already configured in Tailscale, Gust probes
up to 64 successive ports without overwriting existing Serve settings. The
same foreground session stays open through app restarts, and Gust closes it on
shutdown. Occupancy can change the selected port between Gust invocations.
Tailscale Serve's status check is not atomic with startup: another process
configuring the same port at the same time can still race it.

## Tasks

`--e.before` and `--e.after` wrap `-e` with codegen or asset steps. Before
commands run first, while the old app is still serving. If any before command
fails, Gust aborts the rerun and keeps the old app running. After commands run
once the app is up. Filesystem changes made by tasks are ignored so codegen
does not trigger another rerun. See `docs/before-after.md` for details.

When stdin is a terminal, press `r` to rerun, `s` to pause/resume auto-reload from file watching, `i` to toggle info logs, `D` to toggle browser debug outlines (proxy mode), and `q` or Ctrl-C to quit. Info logs are disabled by default and show the file or directory that triggered a reload. Manual `r` reruns still work while auto-reload is paused. Resuming runs one reload if file changes were missed. Gust also prints its Unix socket path on startup. Agents can send `{"action":"status"}` or `{"action":"rerun"}` as one JSON request per connection.

## Control

Agents and scripts control a running Gust instance with `gust ctl`:

```sh
gust ctl status   # state, ports, version, auto-reload, last exit
gust ctl pause    # pause filesystem auto-reload
gust ctl rerun    # reload now (works while paused)
gust ctl resume   # resume auto-reload
gust ctl logs     # output captured from the last failed exit
gust ctl comments                  # list all unfinished comments (created, submitted, seen)
gust ctl comments --wait           # wait for oldest submitted comment batch
gust ctl comments --pending        # recover seen unfinished comments
gust ctl comments done <id>
gust ctl comments abandon <id> <reason>
gust ctl help
```

`gust ctl` finds the instance through the socket derived from the current directory (`/tmp/gust-<uid>/<hash>.sock`). Use `-S <socket>` to target an explicit socket. Commands print compact text and exit `0` on success, `1` when the instance cannot be reached or the request fails, and `2` on usage errors.

Comment commands emit stable JSON when Gust is launched with `--optin comments`. Without that opt-in, comment socket commands return `comments_disabled`, browser comment API paths return 404, and the injected widget contains only reload/status controls. Opt-in works without proxy mode too, allowing ctl comment workflows without browser submission. `comments --wait` blocks until the oldest submitted batch arrives and atomically marks its comments seen. `comments` lists all unfinished comments (created, submitted, and seen). `comments --pending` lists only seen-but-unfinished comments for recovery after an interrupted agent. Mark each comment `done` or `abandon` it with a reason. Comment data is in-memory and is lost when Gust exits. In proxy mode, the injected browser panel lets viewers select page elements, add comments, and inspect comment history. Ctrl+Enter saves a comment. Comment mode stays active after a reload in the same tab (an open editor returns to selection mode; unsaved text is not retained). Autosubmit is on by default in comment mode and submits each saved comment individually; turn it off to keep drafts, then use Submit to send all drafts as a batch. The newest comments appear first, with drafts, submitted comments, and in-progress comments clearly labeled. Drafts can be removed with the × beside each one. Done and abandoned comments are counted at the bottom instead of shown individually. Pins distinguish created, submitted, and seen comments; done comments have no pins. New pins track the clicked point relative to the selected element, including when it moves or resizes. Pin placement requires a confident element match, otherwise the panel reports that the location was not found. The panel displays comments from other paths as pending. Page backgrounds (body/html) can also be selected; closing the floating editor returns to selection mode. The browser does not start an agent; use the CLI commands above to receive and finish submitted comments.

A typical agent flow is: `gust ctl pause`, edit files, `gust ctl rerun`, then `gust ctl resume`. Pausing only stops filesystem-triggered reloads; manual and `rerun` reloads still work. Resuming runs one reload if file changes were missed while paused.

## Agent skill

Print the general Gust comments skill with `gust skill comments`. For an agent
asked to monitor continuously, use `gust skill comment watch`. Redirect either
output, including its YAML frontmatter, to your agent's skill directory:

```sh
mkdir -p ~/.pi/agent/skills/gust-comments ~/.pi/agent/skills/gust-comment-watch
gust skill comments > ~/.pi/agent/skills/gust-comments/SKILL.md
gust skill comment watch > ~/.pi/agent/skills/gust-comment-watch/SKILL.md
```

## Manual

Run `gust man` for a brief manual with samples. `gust man run` covers the
runner flags, `gust man ctl` covers the control commands.

```sh
gust man
gust man run
gust man ctl
```

The pages are plain text files under `docs/man/` and are embedded into the
binary with `go:embed`.

## V1 limitations

- Linux only.
- One command and one optional app/proxy pair.
- Flags only. No config file.
- No polling watcher.
- No TLS proxy, auth, CORS controls, or multi-app support.
- Browser tooling is available only through the local proxy: full-page reload and a status panel. In-memory element comments are opt-in via `--optin comments`. The panel is not an authentication boundary; anyone who can access the proxy can submit comments when enabled.
