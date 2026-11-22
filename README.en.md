# Zatvor

Russian version: [README.md](README.md)

**Status: in development.** There is nothing to install yet.

Zatvor is a free desktop client that (1) runs Xray-core and accepts what the Happ client accepts, (2) keeps chosen applications from ever using a non-allowed public address (they are frozen, then closed or cut off from the network), and (3) sends Russian traffic directly while AI services, blocked sites and developer tools go through the tunnel. It is aimed at non-technical users in Russia who paste a subscription link, and at developers running Claude Code, Codex and Gemini CLI.

No telemetry. No referral links.

Licence: GPL-3.0, see [LICENSE](LICENSE). Third-party components: [NOTICE](NOTICE).

## What Zatvor sends

Zatvor contacts a subscription server only when you add or refresh a subscription. There are no automatic background requests in 1a. There is no telemetry.

A request to a provider carries:

- Your subscription URL (it usually holds your token). It is stored only in the encrypted file and never appears in logs or error messages.
- `User-Agent`. The default is `Zatvor/<version> (<OS>)`. A profile can choose `Zatvor/<version> (<OS>; happ-compatible)` or a Happ identity like `Happ/<version>/<OS>/zatvor`. Pick the Happ identity only on purpose: it pretends to be another client.
- `Accept-Encoding: gzip`.
- `x-hwid`: 32 hex characters, computed as HMAC-SHA256 of the machine id and the subscription host ([ADR 0008](docs/decisions/0008-hwid-algorithm.md)). The machine id cannot be recovered from it, and each provider sees a different value. It is always sent, because providers count devices by it. So the first request registers a device with the provider.
- `x-device-os`, `x-ver-os`, `x-device-model`: OS name, OS version and device model. Sent by default. Turn them off with the "send device info" switch (`SendDevice`) in the subscription settings; `x-hwid` stays.

Not sent: user name, computer name, installed programs, cookies. The request ignores the OS proxy settings and goes directly.

If the server redirects to a different host, `x-hwid` and `x-device-*` are not forwarded. No redirect carries a `Referer` header (it would hold the subscription URL and its token). A redirect from https to http is refused.

Server latency test: only when you run it, Zatvor makes an HTTP GET to `https://www.gstatic.com/generate_204` through each tested server (via Xray, not directly from your address). The request carries nothing about you.

Other network use: a verified download of Xray-core from the official GitHub releases (no data about you) and, from stage 1b, a connectivity check through the tunnel. Nothing about you is sent anywhere else.
