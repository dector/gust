# Hub

## Development

Hub lives in the Gust repository at `hub/` as a separate Go module. Run the commands below from `hub/`. The root `go.work` uses the local Gust source for development. The service file assumes the checkout is at `~/gust`; adjust its paths if yours is elsewhere.

- `ror dev` runs Gust with automatically chosen app and live-reload proxy ports (`-TT`). Gust prints the URLs and exposes the proxy through Tailscale Serve when available.
- `ror test` generates templ code and runs all Go tests.
- `ror build` generates templ code and builds `bin/server`.
- `ror deploy` builds and restarts the installed `hub.service` user service.
- `ror serve` runs the app directly on `:8080` without hot reload.
- `ror launch` builds the binary and runs it in the foreground on `127.0.0.1:2222` without Gust. Stop the user service first if it already owns that port.

## User service and Tailscale

From `hub/`, build with `ror build`. Install `deploy/hub.service` as `~/.config/systemd/user/hub.service`. It starts the binary on loopback port 2222, without Gust. The service requires a nonempty `APP_PASSWORD`; `AUTH_PASSWORD` is supported for direct runs but not this service. Set up a private password file:

```bash
umask 077
mkdir -p ~/.config/hub ~/.config/systemd/user
printf 'APP_PASSWORD=hub\n' > ~/.config/hub/hub.env
chmod 600 ~/.config/hub/hub.env
install -m 644 deploy/hub.service ~/.config/systemd/user/hub.service
systemctl --user daemon-reload
systemctl --user enable --now hub.service
# After installing the service, redeploy later with: ror deploy
tailscale serve --bg --https=2222 2222
```

`hub` is easy to remember but easy to guess for anyone on your tailnet; change `APP_PASSWORD` to a stronger value if that matters. Use `loginctl enable-linger "$USER"` if you need the user service to survive logout and start at boot (lingering may require admin permission). Tailscale Serve exposes `https://<your-tailnet-host>:2222/` to your tailnet. The Serve mapping persists independently of the user service; `tailscale serve status` shows it.

## Configuration

`APP_HOST` controls the bind host (default `127.0.0.1`). `APP_PORT` controls the app port; otherwise `PORT` is used, then `8080`. Set `APP_PASSWORD` to choose the login password (`AUTH_PASSWORD` is also accepted). If neither is set, the password is `hub`. `/health` is public; the home page and other routes require login.

## Gust instances

The authenticated home page shows compact cards for Gust instances discovered with Gust's `pkg/probe` API. The **Rescan instances** button refreshes the cards asynchronously using a Datastar SSE patch, without reloading the page. Card titles show paths relative to your home directory (for example, `/home/pi/ced` becomes `ced`), and the full workdir appears on hover. Cards show state and app/proxy ports. It checks each running app's `GET /` on loopback with a one-second timeout (without following redirects); responding apps appear first, and unavailable instances remain at the bottom. Reported Tailscale URLs are the main links. When one is missing, Hub links to `https://<most-common-reported-ts.net-hostname>:<app-port>/` if a valid app port and a reported hostname exist. This inferred link may not work if Tailscale Serve does not expose that port. Discovery uses Gust's Go library directly, so the service needs no Gust executable on its `PATH`. Scan errors appear on the page. The index uses `Cache-Control: no-store` because it contains local workdirs and Tailscale URLs.

## Datastar status check

The home page's **Check server** button calls authenticated `/status`. The server sends a one-shot Server-Sent Event patch to `#server-status`. The Datastar v1.0.2 JavaScript is vendored at `assets/datastar-v1.0.2.js` and served locally, so no CDN is needed. The SSE response uses `Cache-Control: no-store`.
