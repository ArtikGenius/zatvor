# Zatvor

Russian version: [README.md](README.md)

**Status: in development.** There is nothing to install yet.

Zatvor is a free desktop client that (1) runs Xray-core and accepts what the Happ client accepts, (2) keeps chosen applications from ever using a non-allowed public address (they are frozen, then closed or cut off from the network), and (3) sends Russian traffic directly while AI services, blocked sites and developer tools go through the tunnel. It is aimed at non-technical users in Russia who paste a subscription link, and at developers running Claude Code, Codex and Gemini CLI.

No telemetry. No referral links.

Licence: GPL-3.0, see [LICENSE](LICENSE). Third-party components: [NOTICE](NOTICE).

## What Zatvor sends

Zatvor contacts a subscription server when you add or refresh a subscription, at daemon start only if you turned on update-at-start (off by default), and on a schedule while the tunnel is up (see below). There is no telemetry.

A request to a provider carries:

- Your subscription URL (it usually holds your token). It is stored only in the encrypted file and never appears in logs or error messages.
- `User-Agent`. The default is `Zatvor/<version> (<OS>)`. A profile can choose `Zatvor/<version> (<OS>; happ-compatible)` or a Happ identity like `Happ/<version>/<OS>/zatvor`. Pick the Happ identity only on purpose: it pretends to be another client.
- `Accept-Encoding: gzip`.
- `x-hwid`: 32 hex characters, computed as HMAC-SHA256 of the machine id and the subscription host ([ADR 0008](docs/decisions/0008-hwid-algorithm.md)). The machine id cannot be recovered from it, and each provider sees a different value. It is always sent, because providers count devices by it. So the first request registers a device with the provider.
- `x-device-os`, `x-ver-os`, `x-device-model`: OS name, OS version and device model. Sent by default. Turn them off with the "send device info" switch (`SendDevice`) in the subscription settings; `x-hwid` stays.

New subscriptions are created with the device headers on (`SendDevice=true`, [ADR 0008](docs/decisions/0008-hwid-algorithm.md)); the switch to turn them off arrives with the panel in stage 1c.

Not sent: user name, computer name, installed programs, cookies. The request ignores the OS proxy settings. Adding a subscription, pressing refresh and the optional refresh at daemon start (setting `UpdateOnStart`, off by default) go through the tunnel when it is up and directly otherwise. Scheduled updates (every 24 hours by default, or the provider's interval, never more often than hourly) go only through the tunnel: with no tunnel they send nothing and are tried again a minute later.

If the server redirects to a different host, `x-hwid` and `x-device-*` are not forwarded. No redirect carries a `Referer` header (it would hold the subscription URL and its token). A redirect from https to http is refused.

Server latency test: only when you run it, Zatvor makes an HTTP GET to `https://www.gstatic.com/generate_204` through each tested server (via Xray, not directly from your address). The request carries nothing about you.

Tunnel check: on every connect, on `zatvor check` and in those recovery cases, Zatvor asks three public IP-check services for your address through the tunnel: `https://api.ipify.org`, `https://ifconfig.co/ip` and `https://icanhazip.com`. All three are queried in parallel, once per check, with a 10 second limit each. The check is OK when two of them name the same address. Each request is a plain GET with no cookies and no identifiers; the services see only the tunnel's exit address. The same check, with the same three services, also runs when Zatvor recovers by itself: after Xray restarts following a crash, and after a geo-set switch and its rollback. While recovering (a check failed or the proxy could not be applied), the check repeats with growing intervals up to 5 minutes: after 10 seconds, then 20, 40 and so on. There is no timer that checks while everything works.

Geo data: the first connect with a Happ profile that names geo files Zatvor does not bundle downloads them through the tunnel from the URLs in that profile (https only). Bundled geo files are checked against pinned SHA-256 values.

Other network use: a verified download of Xray-core from the official GitHub releases (no data about you). Nothing about you is sent anywhere else.
