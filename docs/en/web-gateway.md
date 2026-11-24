# Browser client

[中文](../zh/web-gateway.md)

`remohop web` serves a browser client (remote desktop, terminal, files, clipboard and info) from this machine:

```bash
remohop web
remohop web --target dev
```

The page is in English or Simplified Chinese: it follows the browser's language (Chinese for any `zh-*`), and the 中文 / English switch in its header changes it at once and is remembered in that browser. `?lang=zh` or `?lang=en` in the address also picks it.

By default it listens on `127.0.0.1` and opens a URL containing an unguessable token, which is the login. A browser cannot speak the P2P session itself, so this process is the client and sees everything you do in the page. Any non-loopback `--listen` address requires `--tls-cert`, `--tls-key` and `--password-file`; a shared gateway should use `--no-saved`. See [security.md](../security.md#browser-gateway).

## Exposing it through Cloudflare Tunnel

To use `remohop web` from outside your network, keep it on a fixed loopback port, point `cloudflared` at it and put a Cloudflare Access policy in front. `--public-url` tells the gateway the https address browsers use, so it accepts the `Host` and `Origin` headers the tunnel passes through. Unit tests simulate tunnelled requests against the gateway; it has not been tried with a real Cloudflare tunnel.

```bash
remohop web --listen 127.0.0.1:8470 --password-file /etc/remohop/web-pass \
        --public-url https://remohop.example.com --no-open --no-saved
cloudflared tunnel run --url http://127.0.0.1:8470 <tunnel-name>
```

`--public-url` requires a loopback listen address and `--password-file`. For requests through the public address, the login rate limiter tells visitors apart by the `CF-Connecting-IP` header Cloudflare sets.

Programs (not browsers) can skip the login call and send `Authorization: Bearer <gateway password>` on every request. This suits a proxy that injects the header so the program never sees the password, for example next to Cloudflare Access service-token headers. Such requests share one gateway session and need no cookie, CSRF token or Origin; failures count against the login rate limit.

- In Cloudflare Zero Trust, create an Access application for the hostname and allow only your own identity or domain.
- Keep `remohop web`'s own login on (`--password-file`). Access sits in front, but the gateway still sees everything in the page.
- The leg between the browser and the gateway passes through Cloudflare, which terminates TLS. Cloudflare can see the screen, keystrokes, files and any host password typed into the page. Do this only if you accept that.
- Add `--no-saved` for shared use. Otherwise everyone who logs in can use the gateway's saved pairings.
- Cloudflare Tunnel carries HTTP(S) only. The rendezvous server itself uses UDP; its websocket bridge, for machines where only HTTPS gets out, can go through a tunnel (see [Networks that allow only HTTPS](self-hosting.md#networks-that-allow-only-https-websocket-bridge)).
