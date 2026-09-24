# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Tiny HTML server for the Gust reload demo."""

import os
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            body = b"ok\n"
            content_type = "text/plain; charset=utf-8"
        elif self.path == "/":
            body = b"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Gust demo</title>
  <style>
    :root { color-scheme: dark; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      background: #171717;
      color: #fafafa;
      font: 1rem/1.6 system-ui, sans-serif;
    }
    main { padding: 2rem; }
    h1 { margin-bottom: .5rem; }
    p { color: #a3a3a3; }
  </style>
</head>
<body>
  <main>
    <h1>Hello from Gust!</h1>
    <p>Edit server.py and refresh automatically.</p>
  </main>
</body>
</html>"""
            content_type = "text/html; charset=utf-8"
        else:
            self.send_error(404)
            return

        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    port = int(os.environ.get("GUST_APP_PORT", "8000"))
    print(f"Serving on http://127.0.0.1:{port}", flush=True)
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
