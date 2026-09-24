# Gust demo

Requires `ror`, `uv`, and Go on your PATH. The task runs Gust from this repo's
source. From this directory, run:

```sh
ror dev
```

Open the `proxy: http://127.0.0.1:<port>` URL printed by Gust (or the HTTPS
URL printed by Tailscale Serve). Edit the HTML in `server.py` and save; Gust
restarts the server and reloads the page. Gust selects path-stable app and proxy
ports when available. Press `q` or Ctrl-C to stop.

## Developing Gust itself

To watch Gust's own source, rebuild it, and restart this demo in one loop, run
the root task from the repository root:

```sh
ror dev:self
```

It builds the new binary before stopping the running Gust, so a failed build
keeps the current session alive. It reuses path-stable app and proxy ports when
available, keeping the Tailscale URL stable, and passes `-T` (Tailscale Serve). Gust keyboard commands work
normally; `q` or Ctrl+C restarts Gust, and a second Ctrl+C stops the wrapper. Extra Gust flags can be
appended, for example `ror dev:self -v`.
