# Gust Ally Protocol (GAP) — experimental design

GAP is a proposed protocol for Gust to discover and manage optional tools. It is **not implemented**. Gust is the **Leader**: it starts and owns the app and an optional foreground **Ally** such as `serv`.

The first capability is `gust:expose-tailscale/v1`. `gust:` means Gust defines this experimental contract, not that only Gust or `serv` can participate.

## Discovery and connection

Gust locates a configured optional Ally on `PATH`. It does not assume the Ally's command-line flags. It launches the candidate with `GUST_ALLY_PROTOCOL=1`; the candidate prints one newline-terminated JSON object and exits:

```json
{"gap":1,"r":{"cn":{"t":"arg","v":"--gap"}}}
```

The argument is supplied by the Ally; `--gap` is only an example. A recognized unsupported version returns `{"gap":null,"e":{"code":"unsupported_version","supported":[1]}}` and exits. Gust does not retry another version. Discovery has a timeout; silence or invalid output means the program is not a GAP Ally. A missing optional Ally should not prevent the app from starting (the exact failure policy is TBD).

Gust then starts a **new, foreground** Ally process using the advertised argument and keeps its stdin/stdout pipes open. GAP messages are newline-delimited JSON on these pipes; logs use stderr. The Ally must not daemonize.

## Capability check

On the connected child, Gust asks only about the capability it needs:

```json
{"i":1,"m":"has-capability","p":{"name":"gust:expose-tailscale/v1"}}
{"i":1,"r":{"has":true}}
```

`{"i":1,"r":{"has":false}}` means unsupported; an `e` response indicates failure to handle the request. Support does not guarantee that Tailscale is installed or ready. The `/v1` suffix versions this capability independently of GAP v1. Discovery does **not** advertise capabilities.

## App readiness and exposure

Gust already owns the app and knows its port (`-p`). With `-h`, Gust waits for the health check; exposure starts at the same readiness point that precedes `--e.after`. The after hook remains a short-lived task, **not** the owner of the Ally. For this first experiment, require `-p` and `-h` for exposure rather than silently interpreting app spawn as readiness. Config syntax for selecting the Ally and capability is still TBD.

After readiness, Gust sends the app's port to the Ally:

```json
{"i":2,"m":"start","p":{"cap":"gust:expose-tailscale/v1","for":":3000","port":"stable-random"}}
{"i":2,"r":{"url":"https://example.ts.net"}}
```

For this capability, `for: ":3000"` means HTTP on `127.0.0.1:3000`, including WebSocket upgrades. Gust sends the **app port**, not its optional reload-proxy port. `port` selects the Tailscale HTTPS port; stable-random selection still needs a definition. `start` replies once the public URL is available; startup failures return `e` with the same `i`. Use a bounded timeout. Only one exposure per Ally connection is proposed for now.

On shutdown, Gust requests stop, waits for the reply, closes the Ally's stdin, and waits for its exit:

```json
{"i":3,"m":"stop"}
{"i":3,"r":{}}
```

If Gust exits unexpectedly, stdin EOF tells the Ally to stop the exposure, reap its own children, and exit; its children must not inherit that pipe. Gust should clean up the Ally if the app or Ally fails, and bound graceful shutdown before force-terminating a stuck child. Parent death does not automatically kill descendants: `SIGKILL`, crashes, and stale Tailscale Serve state need testing.

## Open decisions

- Configuration syntax and whether optional Ally failures are warnings or fatal.
- Keep the exposure connected across app restarts on the same port, or stop and restart it? Keeping it avoids changing the public URL but may briefly proxy to an unavailable app.
- How `stable-random` selects and retains an HTTPS port.
- How Gust reports the URL and exposure failures to users and `gust ctl`.
- What to do if the Ally exits while the app remains healthy.
