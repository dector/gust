# Gust: Before / After Tasks

This document describes Gust's pre/post task feature: `--e.before` and
`--e.after`. It is the first v2 feature layered on the v1 runner, whose
original docs now live under `docs/archive/`.

## Motivation

Real projects need code generation before a rerun: `templ generate`,
`sqlc generate`, `go generate`, asset builds. Today users cram those into
`-e`, so a failed codegen still restarts the server. Gust needs to run work
*before* touching the running app, and skip the restart when that work fails.

## CLI surface

```text
-e <cmd>              required command, executed via /bin/sh -c
--e.before <cmd>      command to run before each rerun, repeatable, fail-fast
--e.after <cmd>       command to run after each start, repeatable
-p <port>             app port, or app:proxy ports
-h <path>             health endpoint path, requires app port
--exclude <path>      path exclude, repeatable
--exclude.glob <glob> glob exclude, repeatable
-v                    verbose Gust logs
```

Naming convention: single-character flags use a single dash (`-e`, `-p`,
`-h`, `-v`); multi-character flags use a double dash (`--e.before`,
`--e.after`, `--exclude`, ...).

There is no config file. This feature stays flags-only.

## Rerun sequence

For every trigger (initial startup, filesystem, `r`, or `gust ctl rerun`):

```text
trigger
  ├─ run --e.before commands, in order, fail-fast
  │    (the old process is still running and serving)
  │    └─ any failure
  │         → abort the rerun
  │         → keep the old process untouched
  │         → log, browser banner, record failure for `gust ctl logs`
  ├─ stop the old process
  ├─ start -e
  ├─ if -h is set: wait for health to pass
  ├─ run --e.after commands, in order, fail-fast
  └─ increment browser version / notify reload
```

`before` commands run before Gust stops the old process. This is the whole
point of the feature: if codegen fails, the healthy old server keeps running.

When no old process is running (first start, or the previous process already
exited), `before` still runs first; a failure just leaves Gust idle.

When neither `-h` nor a ready check exists, `--e.after` runs immediately
after `-e` spawns, without waiting for the 300ms stability window.

## Failure semantics

- **`before` failure**: abort the rerun. Do not stop or restart the app.
  State stays `running` if a process was running, otherwise `stopped`.
  Show a browser banner and record the failed command for `gust ctl logs`.
- **`after` failure**: log and show a browser banner. The app keeps running
  and the browser reload still fires. `after` is informational.
- Task exit is a failure whenever the command exits non-zero or fails to
  start.

## Triggers while tasks run

- Filesystem triggers are suppressed while tasks run and while the run they
  started is coming up. Codegen output must not cause a second rerun or a
  loop.
- Manual `r` and `gust ctl rerun` still work. They coalesce into one pending
  rerun, matching the existing "trigger during restart" behavior.
- If `before` fails, a coalesced pending rerun is dropped to avoid a
  failure loop. The user can trigger again.

## Shutdown

A running task is terminated on shutdown using the same process-group
stop logic as the app (SIGTERM, then `2s`, then SIGKILL). A half-finished
codegen must not block quit.

## Process and environment

- Each task runs through `/bin/sh -c` in the project root.
- Tasks inherit the same environment as `-e`, including `GUST`,
  `PORT` (when an app port is set), and `GUST_PROXY_PORT`.
- `before` / `after` output streams raw to the terminal.
- Tasks have no timeout. A stuck task blocks the rerun until quit or a new
  trigger (which coalesces, it does not preempt).

## Logs and status

- A failed task's output is stored in the same last-failure slot used by
  `gust ctl logs`, tagged with the phase and command:

```json
{"ok":true,"code":1,"phase":"before","command":"templ generate","stdout":"...","stderr":"..."}
```

- `gust ctl logs` prints the phase and command when present.
- `status` is unchanged apart from the optional phase/command fields on
  `last_exit`.

## Health and readiness

- `-h` remains the only readiness mechanism. There is no `--e.ready`.
- Health timeouts and intervals stay fixed at `10s` / `200ms`.
- When `-h` is set, `--e.after` runs after health succeeds.
- When `-h` is not set, `--e.after` runs immediately after spawn.

## Out of scope

- Change-detection rules: tasks run unconditionally on every rerun.
- Named tasks, dependencies, or a task graph.
- Config files.
- Configurable task or readiness timeouts.
- Conditional execution based on which files changed.

## Testing

- CLI: repeatable `--e.before` / `--e.after` parse in order; usage shows the
  double-dash names; empty values are rejected.
- Coordinator (fake runner): `before` failure aborts and leaves the old
  process running with state `running`; `before` success stops then starts;
  `after` failure does not stop the app; filesystem triggers are suppressed
  during tasks; manual triggers coalesce.
- Logs: a failed task's output is returned by `gust ctl logs` with phase.
- Integration: a failing `before` keeps the old server up.
