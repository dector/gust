# Gust demo

Requires `ror`, `uv`, and Go on your PATH. The task runs Gust from this repo's
source. From this directory, run:

```sh
ror dev
```

Open http://127.0.0.1:8001 (Gust's reload proxy). Edit the HTML in
`server.py` and save; Gust restarts the server and reloads the page. The app
itself listens on port 8000. Press `q` or Ctrl-C to stop.
