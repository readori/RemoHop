# Browser client

[中文](../zh/web-gateway.md)

`remohop web` serves a browser client (remote desktop, terminal, files, clipboard and info) from this machine. Its remote desktop has the standalone viewer's pointer drawn by the page, mouse lock and audio ([Screen and remote desktop](remote-desktop.md)):

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
        --public-url https://remohop.example.com --real-ip-header CF-Connecting-IP \
        --no-open --no-saved
cloudflared tunnel run --url http://127.0.0.1:8470 <tunnel-name>
```

`--public-url` requires a loopback listen address and `--password-file`. For requests through the public address, the login rate limiter tells visitors apart by the header named with `--real-ip-header` (`CF-Connecting-IP`, which Cloudflare sets and overwrites). Name only a header your proxy always sets: anyone can send any header. Without the flag, every visitor through the proxy shares one rate limit, and the gateway says so when it starts. A visitor that logged in from the same address within 30 days is not held back by the global failure budget that strangers' wrong passwords use up, only by its own lockout.

- In Cloudflare Zero Trust, create an Access application for the hostname and allow only your own identity or domain.
- Keep `remohop web`'s own login on (`--password-file`). Access sits in front, but the gateway still sees everything in the page.
- The leg between the browser and the gateway passes through Cloudflare, which terminates TLS. Cloudflare can see the screen, keystrokes, files and any host password typed into the page. Do this only if you accept that.
- Add `--no-saved` for shared use. Otherwise everyone who logs in can use the gateway's saved pairings.
- Cloudflare Tunnel carries HTTP(S) only. The rendezvous server itself uses UDP; its websocket bridge, for machines where only HTTPS gets out, can go through a tunnel (see [Networks that allow only HTTPS](self-hosting.md#networks-that-allow-only-https-websocket-bridge)).

## Programs (bearer tokens)

Program access is off by default. Start the gateway with `--api-token-file FILE` (mode 600, at least 8 characters; needs `--password-file`) and programs (not browsers) can skip the login call and send `Authorization: Bearer <token>` on every request. This suits a proxy that injects the header so the program never sees the token, for example next to Cloudflare Access service-token headers. Such requests share one gateway session and need no cookie, CSRF token or Origin; failures count against the login rate limit. Without `--api-token-file` a bearer request is answered with `bearer_disabled`.

```bash
remohop web --listen 127.0.0.1:8470 --password-file /etc/remohop/web-pass \
        --api-token-file /etc/remohop/web-api-token \
        --public-url https://remohop.example.com --no-open --no-saved
```

The gateway cannot see who added an `Authorization` header. A bearer request that carries what browsers send (`Origin`, `Sec-Fetch-Site` or `Sec-Fetch-Mode`, cookies) therefore gets the browser checks as well: it is refused if it comes from another site, a state-changing call needs the CSRF token from `POST api/v1/session`, and a WebSocket or download with an `Origin` must come from the gateway's own address. A program that keeps cookies (for example Cloudflare's `CF_Authorization`) either drops them for the gateway or sends the CSRF token too.

**If a proxy injects the token, scope it to program traffic only.** Do not add the header to everything that reaches the hostname: browsers would then be logged in by the proxy, and the checks above are the only thing between a hostile page and your gateway (old browsers that send no `Origin` or `Sec-Fetch-*` headers are not covered by them). Give programs their own hostname or path and inject the header only there, and only for the program's identity (in Cloudflare, a rule that matches the service token, not the Access policy that lets your browser in). Browsers keep using the login form.

**Migrating.** Before `--api-token-file` existed, the gateway accepted the gateway password as the bearer token whenever it had `--password-file`. Such a deployment stops working (its programs get `bearer_disabled`) until you add `--api-token-file`. Pointing it at the password file keeps the old token working; better, create a new token, update the proxy rule that injects it, and keep the password for browsers.
