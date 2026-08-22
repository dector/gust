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
gust -e 'go run ./cmd/server' -p 8080
gust -e 'go run ./cmd/server' -p 8080:5000
gust -e 'go run ./cmd/server' -p 8080:5000 -h /health
gust -e 'go run ./cmd/server' -p 8080 --exclude frontend/node_modules -v
gust -e 'go run ./cmd/server' --exclude.glob '*_templ.go'
```

Flags:

- `-e <cmd>`: required command. Gust runs it with `/bin/sh -c`.
- `-p <port>`: optional app port, or `app:proxy` ports.
- `-h <path>`: optional health endpoint. Requires an app port.
- `--exclude <path>`: repeatable watched-path exclude.
- `--exclude.glob <glob>`: repeatable glob exclude for watched events.
- `-v`: verbose Gust logs.

`--exclude` values are relative path prefixes. Use them for directories or whole subtrees, for example `--exclude frontend/node_modules`.

`--exclude.glob` values use Go filepath glob syntax and are matched against both the project-relative path and the file basename. The pattern must match the whole value. `*` does not cross `/`, so `assets/*.tmp` matches `assets/cache.tmp` but not `assets/nested/cache.tmp`. A basename glob like `*_templ.go` matches files with that name pattern in any directory.

When stdin is a terminal, press `r` to rerun and `q` or Ctrl-C to quit. Gust also prints its Unix socket path on startup. Agents can send `{"action":"status"}` or `{"action":"rerun"}` as one JSON request per connection.

## V1 limitations

- Linux only.
- One command and one optional app/proxy pair.
- Flags only. No config file.
- No polling watcher.
- No TLS proxy, auth, CORS controls, or multi-app support.
- Browser support is limited to full-page reload and a simple error banner.
