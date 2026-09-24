# Gust demo

Requires `ror`, `uv`, and Go on your PATH. The task runs Gust from this repo's
source. From this directory, run:

```sh
ror dev
```

Open http://127.0.0.1:8001 (Gust's reload proxy). Edit the HTML in
`server.py` and save; Gust restarts the server and reloads the page. The app
itself listens on port 8000. Press `q` or Ctrl-C to stop.

## Developing Gust itself

To watch Gust's own source, rebuild it, and restart this demo in one loop, run
the root task from the repository root:

```sh
ror dev:self
```

It builds the new binary before stopping the running Gust, so a failed build
keeps the current session alive. It uses ports 8000:8001 and passes `-T`
(Tailscale Serve), matching the shared VPS setup. Extra Gust flags can be
appended, for example `ror dev:self -v`.
