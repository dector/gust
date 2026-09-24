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
    :root { color-scheme: dark; font-family: system-ui, sans-serif; }
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      padding: 3rem 2rem;
      overflow-x: hidden;
      color: #fff7e9;
      background: #24180f;
      line-height: 1.6;
    }
    .sky { position: fixed; inset: 0; overflow: hidden; pointer-events: none; }
    .sky::before, .sky::after {
      content: "";
      position: absolute;
      width: 70vmax;
      height: 70vmax;
      border-radius: 50%;
      filter: blur(65px);
      opacity: .35;
      animation: wander 18s ease-in-out infinite alternate;
    }
    .sky::before { top: -40vmax; left: -20vmax; background: radial-gradient(circle, #d87938, transparent 65%); }
    .sky::after { right: -25vmax; bottom: -40vmax; background: radial-gradient(circle, #a08d46, transparent 65%); animation-delay: -9s; }
    .sky svg { position: absolute; width: 100%; height: 100%; opacity: .4; }
    .sky path { fill: none; stroke: #f9bb71; stroke-width: 2; stroke-linecap: round; stroke-dasharray: 180 1100; animation: stream 9s linear infinite; }
    .sky path:nth-child(2) { animation-delay: -3s; }
    .sky path:nth-child(3) { animation-delay: -6s; }
    body.wind-off .sky::before, body.wind-off .sky::after,
    body.wind-off .sky path, body.wind-off .sky .spark { animation: none; }
    .spark { position: absolute; width: 5px; height: 5px; border-radius: 50%; background: #fbd18e; box-shadow: 0 0 20px #ed9a50; animation: drift 12s linear infinite; }
    .spark:nth-of-type(1) { top: 23%; left: 12%; }
    .spark:nth-of-type(2) { top: 65%; left: 25%; animation-delay: -4s; }
    .spark:nth-of-type(3) { top: 42%; left: 8%; animation-delay: -8s; }
    .page { position: relative; width: min(100%, 900px); }
    main {
      position: relative;
      width: 100%;
      padding: clamp(2rem, 7vw, 4rem);
      border: 1px solid #ffffff30;
      border-radius: 2rem;
      background: linear-gradient(135deg, #ffffff18, #ffffff05);
      box-shadow: 0 30px 100px #0009, inset 0 1px #ffffff30;
      backdrop-filter: blur(18px);
      animation: arrive 1s ease-out both;
    }
    .hero-heading { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
    .badge { display: inline-flex; align-items: center; gap: .6rem; color: #ffd18d; font-size: .8rem; font-weight: 700; letter-spacing: .14em; text-transform: uppercase; }
    .wind-toggle { flex: none; padding: .45rem .75rem; border: 1px solid #ffffff30; border-radius: .65rem; background: #ffffff0d; color: #fff3de; font: inherit; font-size: .85rem; cursor: pointer; }
    .wind-toggle:hover, .wind-toggle:focus-visible { background: #ffffff18; }
    .wind-toggle:focus-visible { outline: 2px solid #f9bb71; outline-offset: 2px; }
    @media (max-width: 420px) { .hero-heading { align-items: flex-start; flex-direction: column; } }
    .badge::before { content: ""; width: .55rem; height: .55rem; border-radius: 50%; background: #f9bb71; box-shadow: 0 0 18px #f9bb71; animation: glow 2s ease-in-out infinite; }
    h1 { margin: 1.5rem 0 1rem; font-size: clamp(3rem, 9vw, 5rem); line-height: 1.05; letter-spacing: -.065em; }
    h1 span { background: linear-gradient(90deg, #ffc172, #e7a35e, #ffc172); background-size: 200% auto; background-clip: text; -webkit-text-fill-color: transparent; animation: shimmer 5s linear infinite; }
    .lead { max-width: 30rem; color: #e0cfba; font-size: 1.1rem; }
    .instruction { display: inline-block; margin: 1.2rem 0 0; padding: .8rem 1.2rem; border: 1px solid #f9bb7155; border-radius: .9rem; background: #49301cbb; color: #fff3de; }
    code { color: #ffca81; }
    .extras { margin-top: 2.5rem; }
    .extras h2 { margin: 0 0 1rem; font-size: 1.4rem; letter-spacing: -.03em; }
    .steps { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1rem; }
    .step, .quickstart {
      border: 1px solid #ffffff24;
      border-radius: 1.2rem;
      background: #ffffff0d;
      backdrop-filter: blur(12px);
    }
    .step { padding: 1.4rem; }
    .step strong { display: block; margin-bottom: .4rem; color: #ffca81; }
    .step p { margin: 0; color: #e0cfba; font-size: .9rem; }
    .quickstart { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-top: 1rem; padding: 1.2rem 1.4rem; }
    .quickstart p { margin: 0; color: #e0cfba; font-size: .9rem; }
    .quickstart button { flex: none; padding: .55rem .9rem; border: 1px solid #ffca8180; border-radius: .65rem; background: #49301c; color: #fff3de; font: inherit; cursor: pointer; }
    .quickstart button:hover, .quickstart button:focus-visible { background: #704423; }
    @keyframes wander { to { transform: translate(20vw, 12vh) scale(1.25); } }
    @keyframes stream { from { stroke-dashoffset: 1280; } to { stroke-dashoffset: 0; } }
    @keyframes drift { from { transform: translate(-15vw, 6vh); opacity: 0; } 15%, 85% { opacity: 1; } to { transform: translate(95vw, -8vh); opacity: 0; } }
    @keyframes arrive { from { transform: translateY(25px); opacity: 0; } to { transform: translateY(0); opacity: 1; } }
    @keyframes glow { 50% { opacity: .4; box-shadow: 0 0 5px #f9bb71; } }
    @keyframes shimmer { to { background-position: 200% center; } }
    @media (prefers-reduced-motion: reduce) { *, *::before, *::after { animation: none !important; } }
    @media (max-width: 600px) { .steps { grid-template-columns: 1fr; } .quickstart { flex-wrap: wrap; } }
    @media (max-width: 420px) { body { padding: 1rem; } }
  </style>
</head>
<body>
  <div class="sky" aria-hidden="true">
    <svg viewBox="0 0 1200 800" preserveAspectRatio="none">
      <path d="M-100 190 C180 60 300 320 580 170 S950 90 1300 130"/>
      <path d="M-100 440 C180 300 340 540 620 390 S960 300 1300 420"/>
      <path d="M-100 680 C180 560 380 730 680 580 S990 510 1300 610"/>
    </svg>
    <i class="spark"></i><i class="spark"></i><i class="spark"></i>
  </div>
  <div class="page">
    <main>
      <div class="hero-heading">
        <div class="badge">The flow starts here</div>
        <button type="button" class="wind-toggle" id="wind-toggle" aria-pressed="true">Wind: on</button>
      </div>
      <h1>Hello from <span>Gust!</span></h1>
      <p class="lead">Gust watches your project files while you work. When you save a change to this demo, it restarts the server and reloads the page for you, so you can see the result right away. No tab switching or manual refresh needed: just edit, save, and keep creating.</p>
      <p class="instruction">Edit <code>server.py</code> and refresh automatically.</p>
    </main>
    <section class="extras" aria-labelledby="steps-heading">
      <h2 id="steps-heading">How it works</h2>
      <div class="steps">
        <div class="step"><strong>01 / Edit</strong><p>Change the HTML in <code>server.py</code>.</p></div>
        <div class="step"><strong>02 / Save</strong><p>Gust detects the change and restarts the app.</p></div>
        <div class="step"><strong>03 / See it</strong><p>Your browser reloads with the new page.</p></div>
      </div>
      <div class="quickstart">
        <div><strong>Try it locally</strong><p>From this directory, run <code>ror dev</code>.</p></div>
        <button type="button" id="copy-command" aria-label="Copy ror dev command">Copy command</button>
      </div>
    </section>
  </div>
  <script>
    const windToggle = document.getElementById('wind-toggle');
    let windEnabled = true;
    try { windEnabled = localStorage.getItem('gust-demo-wind') !== 'off'; } catch (_) {}
    function setWindEnabled(enabled) {
      windEnabled = enabled;
      document.body.classList.toggle('wind-off', !enabled);
      windToggle.setAttribute('aria-pressed', String(enabled));
      windToggle.textContent = `Wind: ${enabled ? 'on' : 'off'}`;
      try { localStorage.setItem('gust-demo-wind', enabled ? 'on' : 'off'); } catch (_) {}
    }
    setWindEnabled(windEnabled);
    windToggle.addEventListener('click', () => setWindEnabled(!windEnabled));

    document.getElementById('copy-command').addEventListener('click', async function () {
      try {
        await navigator.clipboard.writeText('ror dev');
        this.textContent = 'Copied!';
        setTimeout(() => { this.textContent = 'Copy command'; }, 2000);
      } catch (_) {
        this.textContent = 'Copy failed';
        setTimeout(() => { this.textContent = 'Copy command'; }, 2000);
      }
    });
  </script>
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
    port = int(os.environ.get("PORT", "8000"))
    print(f"Serving on http://127.0.0.1:{port}", flush=True)
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
