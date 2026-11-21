# Zatvor: design specification (draft)

Status: **APPROVED by Artur 2026-11-19 (stage 0b); mockups approved 2026-11-19.** Date: 2026-11-19. Source of requirements: `brief/BRIEF.md` (not repeated here; this document records what the brief leaves open, what research changed, and the decisions taken). Research backing every factual claim: `docs/research/xray-core.md`, `docs/research/subscriptions.md`, `docs/known-pitfalls.md`.

Markers: **[DECIDED]** agreed with Artur; **[CALL]** my call, open to correction; **[0c]** settled by a stage 0c prototype; **[OPEN]** waiting for Artur.

---

## 1. Intent

Zatvor is a free GPL-3.0 desktop client that (1) runs Xray-core with full compatibility with what Happ accepts, (2) keeps chosen applications from ever using a non-allowed public address (freeze, then close or cut network), and (3) routes Russian traffic direct while AI services, blocked sites and developer tools go through the tunnel. Audience: a non-technical user in Russia who pastes a subscription link; secondary: developers running Claude Code, Codex, Gemini CLI.

The overriding goal is stability: lost internet, apps frozen forever, or traffic leaking past the tunnel are worse than any missing feature.

### Goals
- Paste a link, press one button, it works in 2–3 minutes without docs.
- No leak of the protected apps' traffic with a non-allowed address, including after crash, sleep, network change, update.
- Every system change is journaled and reverted on exit, crash, next start and uninstall.
- Windows 10/11 first; macOS 13+ (universal) and Linux on the same architecture.

### Non-goals
Cloud, accounts, telemetry, Telegram, remote control, IP reputation via a server, chat backup, remote version kill-switch, `happ://crypt*` decryption, sing-box/mihomo as protocol core, WSL routing (for now).

### Anti-goals (it "works" but is a failure)
- The app silently rewrites a provider's routing/dns.
- A green status when a check could not run.
- Protection that depends on something the user can't see (an auto-filled allow-list, a hidden trusted network).
- Antivirus-looking behaviour (hidden PowerShell, DNS TXT polling, killing processes by name).
- Several subscription requests per user action (provider's TSPU bans the server after a burst).

---

## 2. Constraints that shape the design

1. **Developer machine safety (brief §11).** Every component that touches system or user state takes its roots and targets as parameters and has a **safe mode (dry-run)** where system changes and freezing are only journaled. The real-change code paths live behind a build tag (`systemtest`) and run only in Windows Sandbox / VM / CI. Freezing in tests refuses any PID the test did not spawn (checked by parent PID + start time + a per-test token in the environment).
2. **Work split [DECIDED]:** stages 0a–0b in the cloud session (no access to Artur's PC); 0c onward needs Artur's PC for Windows Sandbox via a remote session.
3. **Licensing.** GPL-3.0. Xray-core is MPL-2.0 without the "incompatible with secondary licenses" exhibit: combinable. We ship the unmodified official binary. Kordon code may be borrowed only from `e01c7c4` (GPL-3.0 in-tree) and preferably from `v1.2.2` (`df69bfb`); every borrowed file gets `path@commit` provenance in `NOTICE`, and is checked against `known-pitfalls.md` §0 because it predates all fixes from 1.3.1. proxy-tunnel has no licence: ideas only.
4. **Russia network reality.** GitHub, jsDelivr and IP-check services may be blocked or slow: every remote source has fallbacks and goes through the tunnel when it is up.

---

## 3. Research results that change the brief

| Brief assumption | Finding | Consequence |
|---|---|---|
| 3x-ui gives Happ full Xray JSON on the normal link | Only if the admin set a User-Agent regex for JSON (default empty) or via `/json/` (off by default). Same story in Remnawave (`serveJsonAtBaseSubscription`) and Marzban (`USE_CUSTOM_JSON_FOR_HAPP`). | With a `Zatvor/x` UA most providers return base64 links, so profiles that need finalmask (Hysteria2-DNS) are lost. See §5.3 **[DECIDED]**. |
| HWID not in 3x-ui main | Merged in PR #5802 (2026-08-15): `limitHwid`, `client_hwids`, `X-HWID` etc.; rejection = 404 empty body + `X-Hwid-*` headers. | Support it as a first-class case. |
| Remnawave without `x-hwid` → 404 | Docs say 404; code returns 200 with empty or placeholder body plus `x-hwid-not-supported` / `x-hwid-max-devices-reached`. `x-hwid` must match `^[a-zA-Z0-9=-]{10,64}$`. subscription-page drops requests without User-Agent. | Handle both; HWID format fits the regex; always send UA. |
| Legacy `noises` removed from Xray | Deprecated in v26.10.31, still parsed on main; a mechanical translation exists and passes `-test` with no warnings on v26.11.9. | Run legacy as is while the pinned Xray accepts it; migration implemented and tested, switched on only when Xray removes `noises` (§6.4). |
| Pin "latest stable" Xray | v26.11.9 (2026-11-09). Fixture `exp` noise needs ≥ v26.9.30, first stable v26.10.21. Unreleased main (#7281) changes freedom/blackhole config. | Pin **v26.11.9** [CALL]; each bump is a reviewed change with the fixture suite. |
| roscomvpn dat is enough | The finalmask fixture needs `geoip:ru` (absent in roscomvpn); the legacy fixture and Happ profile need roscomvpn-only tags (`geoip:direct`, `geosite:win-spy`, `whitelist`, `torrent`, `twitch-ads`). | Ship both and merge (§6.5) [CALL]. |
| `xray -test` validates config | It silently ignores unknown keys. | Own schema validation per pinned Xray version, plus `-test`. |
| AmneziaWG new params are "2.0" | They are AWG 3.1 (amneziawg-go ≥ v3.1.20260812). UAPI rejects `RandomTrailers = on`. | Stage 6 note; map on/off → true/false. |
| Xray TUN as an option | Exists on Win/macOS/Linux, but new stack only since v26.10.21; Windows IPv6/DNS fixes still landing in November; README: "for network professionals". v2rayN defaults to a sing-box TUN front chained to Xray SOCKS. | Prototype both in 0c (§4.3). |

---

## 4. Architecture

### 4.1 Components

```
 ┌────────────── user session (no admin) ───────────────┐      ┌──── privileged (stage 3) ────┐
 │                                                       │      │                              │
 │  zatvor-ui (tray + panel)  ◀── local API ──▶ zatvord  │◀────▶│  zatvor-helper (Windows       │
 │  zatvor (CLI)              ◀── local API ──┘   │  ▲   │ pipe │  service / launchd daemon /  │
 │                                                │  │   │      │  systemd unit): TUN, routes, │
 │  zatvor-supervisor ── restarts ────────────────┘  │   │      │  WFP/pf/nft blocks, DNS      │
 │                                                   │   │      └──────────────────────────────┘
 │  xray (official binary, child of zatvord) ────────┘   │
 └───────────────────────────────────────────────────────┘
```

- **zatvord (Go)** — the brain: profiles, subscriptions, config generation, Xray lifecycle, protection engine (freeze/close), probes, network events, system-proxy and dev-tool integration, journal/restore, local API. Runs as the user. Pure logic packages (`parse`, `gen`, `rules`, `guard`, `journal`) have no OS calls and take clock/network/filesystem interfaces.
- **xray** — separate supervised child process [CALL, see 4.2].
- **zatvor-supervisor** — tiny process (the same binary in supervisor mode) that restarts zatvord with backoff and, if it gives up, releases or closes frozen apps according to policy (K3). Never holds system state itself.
- **zatvor-helper** — stage 3 only. Narrow privileged API: create/destroy TUN, set/restore routes and DNS, add/remove per-app network blocks. Installed in Program Files (the whole app is machine-wide from the first release, §16 Q2.9) / `/Library/PrivilegedHelperTools`; verifies the caller and the signature of updates. Does **not** touch the system proxy (HKCU is per-user, set by zatvord).
- **zatvor-ui** — tray icon, pre-created hidden popup, full panel; a thin client of the local API. A UI crash never affects protection.
- **zatvor CLI** — `status`, `check`, `allow`, `run -- <cmd>`, machine-readable output and stable exit codes.

### 4.2 Xray: separate process vs library [CALL]
Separate process, official signed binary verified against the release `.dgst` at build time. Reasons: an Xray panic (several in 2026: #7369, #6729, #6653) would otherwise kill the daemon that holds TUN, routes, WFP filters and the freeze journal; a separate process lets the daemon restore state and restart Xray with backoff. Cost: IPC via Xray's gRPC API (stats, outbound hot-swap) — acceptable. Library mode rejected; libXray "does not guarantee API stability".

### 4.3 TUN front [0c]
Candidates, both chained so that Xray stays the only protocol engine:
- **A. Xray's own `tun` inbound** (with `autoSystemRoutingTable`, `autoOutboundsInterface`, Windows `autoSystemWfpBlockLeak`). One process, fewer moving parts; young stack.
- **B. sing-tun (Go library) inside zatvor-helper**, forwarding to an Xray SOCKS/`dokodemo` inbound (the v2rayN default architecture). Mature, per-process rules and strict-route; extra hop.
- tun2socks rejected: v2rayN dropped it, sing-tun covers it.
Prototype criteria (Windows Sandbox, then macOS by Artur): DNS leak test incl. IPv6 and smart multi-homed resolution, sleep/wake, kill -9 recovery, adapter leftovers, throughput, CPU idle, per-app rule by process path. Default expectation: B, unless A passes everything.

### 4.4 UI framework [0c]
Candidates: **Tauri 2** (Rust shell, WebView2/WKWebView; Clash Verge Rev proves tray+popup on Win/mac), **Wails v3** (Go, status to verify in 0c — alpha/beta), **systray + webview** (Kordon's stack; Go-only, but the popup is hand-made per OS). Criteria: popup next to the tray that opens instantly from hidden, correct placement on Win10/11 taskbar positions and macOS notch, dark/light following system, no console windows, binary size, RAM of the hidden window, crash isolation from zatvord (UI is a separate process in all three), WebView2 child processes identifiable to exclude from freezing (K1). Expectation: Tauri 2 (mature tray APIs, separate process, our UI never shares a process with the guard).

### 4.5 Local API
HTTP over `127.0.0.1` on a random free port (fallback ports far apart, K50) plus a discovery file; token in a user-only file; `Host` must be `127.0.0.1:<port>`, `Origin` absent or our own; JSON; long-poll/SSE for state. The UI, CLI and later the browser extension use it. On Windows, prefer a named pipe for CLI/UI where the framework allows it [0c].

### 4.6 Process model and single instance
Single instance via a named mutex / lock file; second launch sends "show window" over the API (K43). zatvord starts Xray only after it has journaled what it will change.

---

## 5. Import and subscriptions

### 5.1 Inputs
Clipboard, file drag & drop, `zatvor://` deep link, QR from screen. `http(s)://` in the import box is always a subscription URL; HTTP proxies have their own form. Links: `vless://` (reality, xhttp incl. `extra`, vision, ws, grpc), `vmess://`, `trojan://`, `ss://` (SIP002 and legacy), `hysteria2://`/`hy2://` (salamander, `pinSHA256`), `socks://` (three forms), full Xray JSON (object or array), Happ routing link/JSON. `happ://crypt*`, INCY crypt, age-encrypted bodies: explicit "encrypted link, not supported" error. Reference parser behaviour: v2rayN `ServiceLib/Handler/Fmt/*` at `9bb74a0` (study, not copy — it is GPL-3 C#, we write Go).

### 5.2 Format detection
By body, never by headers: JSON (object/array with `outbounds`) → Xray JSON; base64 (std/url/raw, whitespace stripped) that decodes to lines of known schemes → links; plain lines → links; YAML Clash → explicit "Clash format not supported" error. `#name: value` lines in the body are treated as Happ-style headers.

### 5.3 User-Agent policy [DECIDED]
- Default `Zatvor/<version> (<os>)`. Always send a UA.
- Per-subscription custom UA field.
- **Artur's panel (3x-ui v3.10.0, verified in source at tag `v3.10.0`):** `subJsonAutoDetect=true` with `subJsonUserAgentRegex=(?i)\bhapp\b`, so only UAs containing the word `happ` get JSON (real UAs: `Happ/4.7.2/Windows/<build>`, `Happ/6.4.0/ios/<build>`). `ApplyCommonHeaders` sends `Routing-Enable: true` and `Routing: happ://routing/onadd/<b64>` to **every** client, links or JSON; with JSON the panel also bakes the same routing/dns into each config (`internal/sub/json_routing.go`, RouteOrder default `block,direct,proxy`) — which is why the legacy fixture's routing equals `happ-routing.json`. `subJsonAlwaysArray=false`: a single config may arrive as one object. 3x-ui's `json_routing.go` is a reference (not copied: GPL-3, but we write our own) for the Happ→Xray translation tests.
- **[DECIDED, review B4]** Artur has already changed his panel regex to include `zatvor`, so stage 1 acceptance (Hysteria2-DNS connects) does not depend on the hint. The hint below is still built in stage 1 [CALL] as part of import, because mass users can't edit their provider's panel; it is not on the stage 1 critical path.
- **Hint:** if the response is links and it looks like a Happ-oriented panel (Happ headers present: `routing` (3x-ui sends it to every client when routing is enabled — strongest signal), `announce`, `hide-settings`, `sub-info-*`, `profile-title` with `base64:` from a 3x-ui/Remnawave signature, or a link remark carrying `serverDescription`), the UI shows: "This provider may give the full configuration only to Happ. Fetch it as Happ?" One button switches this subscription's UA and does **one** fetch. Never automatic. Two presets [CALL]: "Zatvor (happ-compatible)" = `Zatvor/<ver> (<os>; happ-compatible)` — matches 3x-ui's `\bhapp\b` without pretending to be Happ; and "Happ" = `Happ/<pinned ver>/<Windows|macos>/zatvor` for panels anchored on `^Happ/` (Remnawave, Marzban). The first is tried by the button; the second only if the user picks it.

### 5.4 Headers we read
`subscription-userinfo` (lenient parse; `expire` unix seconds; 0 = unlimited), `profile-title` (`base64:` or plain), `profile-update-interval` (hours), `support-url`, `profile-web-page-url`, `announce`, `routing` (Happ routing link). **[DECIDED, review B4]** For a links subscription, a `routing` header (with `routing-enable` not false) *is* "as the provider sent it": it is applied immediately as the routing/dns source, shown in the profile as "Provider routing (Happ profile)" and switchable in settings. For a JSON subscription the routing baked into each config is the source and the header is ignored (3x-ui bakes the same rules). A changed header on update is applied like any other provider change and shown in the update diff., `content-disposition` filename as a title fallback, `x-hwid-*` responses (shown as a human message: "device limit reached on this subscription"). Happ mobile/UI-only headers are ignored and listed in the docs.

### 5.5 HWID [CALL]
`x-hwid` = first 32 hex chars of `HMAC-SHA256(key="zatvor-hwid-v1", machine_id ‖ subscription_host)`. machine_id: Windows `MachineGuid`, macOS `IOPlatformUUID`, Linux `/etc/machine-id`. Stable across app reinstalls; not reversible; different per provider host so providers cannot correlate devices. `x-device-os`, `x-ver-os`, `x-device-model` sent by default, switchable off; documented in README "What Zatvor sends". Before the first request to Artur's dev subscription, the app (and I) warn that it registers a device.

### 5.6 Fetch and update rules
- One user action = exactly one request (dedup in-flight, no parallel "try UA variants").
- Forced through the proxy outbound when connected (R3.6); without the tunnel only by the user's button, no automatic retries; `Accept-Encoding: gzip`; timeout 20 s; retries only for scheduled updates, exponential backoff 1→2→4→… min, capped at the update interval.
- Response validated (parse + schema + `xray -test`) before it replaces anything; last working copy kept; a bad update never removes working profiles; the diff (added/removed/changed) is shown.
- Secrets (subscription URLs, keys in profiles) stored encrypted: Windows — data key in Credential Manager, payload DPAPI/AES-GCM; macOS Keychain; Linux Secret Service with file fallback (0600).

---

## 6. Config generation

### 6.1 What we add to a provider's JSON
Only: our inbounds (see 6.2), the Xray `api` section on a random localhost port, our probe outbound tags, `log` settings, and — in TUN mode — the TUN-related inbound. We never rewrite provider `routing`/`dns`; when the user picks another routing source the whole routing/dns comes from that source (§6.3). Every injection is a pure function with golden-file tests.

### 6.2 Inbounds and ports
- `mixed`/`socks`+`http` inbound for system proxy on a free port chosen at start (avoid 10808/10809 and ports owned by others; WSL mirrored mode reservations, K50); persisted so the system proxy setting is stable.
- A separate **authenticated** SOCKS inbound (random user/password per run) for Zatvor's own requests and probes, so other local apps can't use the probe path (pitfall: unauthenticated inbounds in Hiddify/Throne).
- Provider inbounds in a full JSON are replaced by ours; this is documented and shown in the profile details.

### 6.3 Rule order [CALL, tests]
Single visible routing source per profile: (a) "as the provider sent it" (default), (b) a preset (`all through VPN`, `all except Russia`, `selected only`), or (c) a Happ routing profile. (b)/(c) replace the provider's routing and dns entirely.
Final rule list, first match wins:
1. Internal: api inbound → api outbound; **tunnel-probe inbound → the profile's proxy outbound** (the outbound the source's final/default rule points at; with a balancer, the balancer). Route checks do **not** get a rule here: they enter through the same inbound the protected app uses and follow the normal rules (§7.3 b).
2. **Block rules** from every layer (user blocks, per-app blocks, source blocks).
3. Per-app rules (TUN mode: process path/name).
4. Developer-tool rules (AI provider domains → proxy) when that feature is on.
5. The selected source's rules, in their own order (Happ `RouteOrder` respected).
6. Source's default outbound.
Every layer is a pure function; tests assert the exact merged list for fixture combinations.

### 6.4 Legacy noise migration [DECIDED, review R3.1]
Legacy configs run **as is** while the pinned Xray still accepts `noises` (it does in v26.11.9, with a deprecation warning). The translation below is implemented and tested now, but switched on only when we move to an Xray release that removes `noises`, and then logged per profile. It preserves packet bytes exactly (fixed `a3c1` transaction ID stays fixed); randomising the ID (`<r 2>`) is offered separately, never applied silently.

Mechanical translation (`docs/research/xray-core.md` §3): freedom `noises` reached via `sockopt.dialerProxy` → `streamSettings.finalmask.udp` `noise` item appended **last** (after salamander, nearest the wire); remove `dialerProxy` and the freedom outbound if unreferenced; drop empty `noises` arrays. Refuse (keep legacy, warn) for ambiguous cases: existing noise in finalmask, chained dialer proxies, `applyTo`, port-53 redirect, freedom with other settings. Both fixtures pass `-test` and produce equivalent wire behaviour in an integration test against a local Xray server (fake DNS datagram observed before QUIC).

### 6.5 Geo databases [CALL]
- Bundled: roscomvpn geoip+geosite (pinned tags) and v2fly/Xray-rules-dat.
- At config build time we compute the tags each config needs and produce a merged `geoip.dat`/`geosite.dat` per tag set: roscomvpn entries win on name collisions, v2fly fills the rest. (Alternative `ext:file.dat:tag` rewriting rejected: it changes provider configs.)
- Happ `Geoipurl`/`Geositeurl` [CALL, review B7]: if a profile names geo URLs, they are that profile's roscomvpn part. If the file at that URL is already present (same URL, or same SHA-256 as a bundled file) nothing is downloaded. Otherwise the bootstrap from §16 Q2.10 applies once; afterwards the cached file is used and refreshed through the tunnel. Bundled files are always the fallback while a download is pending.
- Updates through the tunnel with SHA-256 verification (roscomvpn publishes `.sha256`), atomic swap, rollback to the previous set if Xray fails to start.
- Unknown tag → human error naming the tag and which database is missing it, before Xray starts.
- Tiny synthetic dat files in the repo for offline tests.

### 6.6 Validation
1. Own schema check against the pinned Xray version (unknown keys → warning, unknown types → error).
2. `xray run -test` on the final config.
3. Only then swap the running config (Xray restart or API hot-swap).

---

## 7. Protection engine

### 7.1 Model
A deterministic automaton per **group** of apps sharing an allow rule (as Kordon, but fixed): states `Starting → OK ⇄ Suspect(frozen) → Closed/Waiting`, plus `Paused` (user). Inputs are events with timestamps and a **network generation** number; the automaton has no I/O and runs on fake clock/network in tests. Property tests: every non-OK state has an exit on a fresh positive probe; no transition to OK from a probe of an older generation; unknown never becomes OK.

### 7.2 Freeze signals
Our tunnel down/reconnecting; Xray exited; probe says IP/country not allowed; IPv4 and IPv6 independently; DNS leak — freeze only when the domains of the protected services themselves resolve outside the allowed path, otherwise a warning [DECIDED, review Q2.8]. Measurement [CALL, review B6]: in our modes by analysing the generated config (which DNS server answers those domains, and is that server reached via the proxy outbound), re-checked on every config change; at runtime the `whoami.akamai.net` resolver check runs through the app's own path. In protection-only mode we cannot see the VPN's DNS routing, so it is a warning only; time zone vs IP country is a warning only, and we never change the system time zone; a protected service's `/cdn-cgi/trace` sees a non-allowed address; OS network event (Win `NotifyAddrChange`/`NotifyRouteChange`, macOS routing socket + SCDynamicStore, Linux rtnetlink); system going to sleep. Because we own the tunnel, "tunnel down" freezes immediately without waiting for a probe.

### 7.3 Probes
- Each probe: new HTTP client, no keep-alive, system proxy/PAC re-read each time, tagged with network generation.
- IP: ≥2 independent services must agree; any answer outside the allowed set (even late) = possible leak; same-country answers in an allowed country agree (K17). Services list configurable, with RU-reachable fallbacks.
- **Two kinds of probes [DECIDED, review Q2.7]:** (a) *tunnel check* — external IPv4/IPv6 forced through the proxy outbound (so a `.ru` IP-check service routed direct by the user's rules can't report the home IP and freeze everything); (b) *route check* — each protected service's `/cdn-cgi/trace` through the same inbound and routing the app itself uses. Freeze decisions for a group are made on (b); (a) diagnoses the tunnel. A missing trace endpoint gives "unknown", never "leak". In "protection only" mode probes go through the system proxy / PAC / `HTTPS_PROXY` / `~/.claude/settings.json` path each app uses.
- AI route check: `https://<service>/cdn-cgi/trace` for the services of running protected apps only, every 2 min and on network change; decisive in split routing.
- DNS: A query for `whoami.akamai.net` at most every 10 min and on network change; never periodic TXT (K23).
- Offline: one retry after ~2 s before declaring offline (K18).
- Cadence [CALL, review B2]:
  - Our tunnel modes: tunnel state is known instantly (Xray process, Xray observatory/health, OS network events), so freezing on tunnel loss needs no polling. While OK: tunnel check every 60 s; route check every 2 min and immediately on connect, profile/routing change, network change and wake. Split-routing misroutes are a property of the config, so they are caught at connect time, not by polling.
  - "Protection only" mode (external VPN we don't control): tunnel check every 5 s as in Kordon; route check every 2 min + events.
  - Trouble: 2 s for the first minute, then 10 s (K24). Events trigger immediately.
  - Documented leak window: our modes ≤ event latency (sub-second) for tunnel loss; protection-only ≤ 5 s + probe time. "What Zatvor sends" lists these rates.

### 7.4 Freezing
- Windows `NtSuspendProcess`, macOS/Linux `SIGSTOP`/`SIGCONT`, whole process tree.
- Identity: PID + creation time; Windows keeps an open handle; Linux pidfd; re-checked before every action (K2).
- Never our own tree (UI incl. WebView2 children, zatvord, supervisor, xray, helper), resolved by identity at runtime (K1).
- **New processes while frozen:** process-table scan every 250 ms while any group is not OK (2 s while all OK) — documented max gap 250 ms + scan time; matching processes are frozen on sight. Platform-native creation notifications (ETW needs admin; WMI is heavy and AV-noisy) are not used in stage 2 [CALL]. In TUN mode (stage 3) the per-app network block makes the gap irrelevant for networking.
- Matching: by executable path, name, or `args:` substring; never only by name for `node.exe`-style hosts unless the user chooses `args:`.
- TTY job control for terminal agents on macOS/Linux (K4).
- Freeze journal on disk (who, when, why); on start, journaled frozen processes are released only after a fresh positive probe; after repeated crashes the supervisor releases or closes per policy (K3).

### 7.5 When the address does not come back **[DECIDED]**
After **60 s** in freeze, close the app **while still frozen** (no unfreeze); "Don't close apps" setting (wait frozen); "Close now" button. Polite close (save prompt) only when that app's network is blocked, i.e. when the privileged service is installed (§16 Q2.4, stage 3).

### 7.6 Allow-list
IPs, subnets, countries; per-app rules; "only through Zatvor profile X". Suggestions from the active profile need confirmation; home IP never auto-allowed; empty list never auto-filled; IPv6 confirmed separately; undo for the last local change (K19–K21). Trusted networks by router MAC, only from the panel with confirmation, warning in proxy mode (K22).

### 7.7 Fail policy
Protected apps fail-closed; everything else fail-open. Xray crash in system-proxy mode: protected apps frozen, system proxy reverted to the pre-Zatvor state, restored after Xray is healthy again.

### 7.8 Proxy-ignoring apps
In system-proxy mode, detect protected apps that bypass the proxy (compare the app's observed route via a probe from a child of the same environment where possible; for known CLIs check their config/env). Such apps are shown as "not protected in proxy mode" with options: TUN (stage 3) or env-var routing (§9). Exact detection method [0c].

---

## 8. System changes, journal and safe mode

- Before changing system proxy, PAC, DNS cache, routes, TUN, shell profiles or app configs, zatvord appends a journal entry (what, previous value, how to revert) and fsyncs it. Revert on normal exit, on next start after a crash/kill/power loss, and by the uninstaller. The supervisor also reverts the system proxy if zatvord cannot be restarted.
- Windows system proxy: HKCU `Internet Settings` (`ProxyEnable`, `ProxyServer`, `ProxyOverride`, `AutoConfigURL`) + `InternetSetOption` refresh; per-connection settings for RAS/VPN connections recorded too. Foreign PAC/proxy never overwritten silently: conflict warning and an explicit choice.
- macOS: per network service via SystemConfiguration (no `networksetup` shell-outs), previous state per service.
- DNS cache flush after network change and after connect (now and +3 s).
- Conflicts: another TUN/VPN adapter, system proxy changed by someone else, foreign PAC → warning card; virtual/container adapters ignored.
- **Safe mode**: a global switch (`--safe` / env / build flag for dev builds) making every system-mutating interface a recorder; the UI shows a permanent "SAFE MODE" badge. On Artur's main PC the app runs only in safe mode.

---

## 9. Developer-tool routing

Same behaviour on Windows and macOS: PowerShell 5.1/7, cmd, Git Bash; zsh, bash.
- Marked blocks in shell profiles (written through symlinks, never replacing the file), reading proxy from a state file so open terminals switch immediately (proxy-tunnel idea).
- `codex`/`gemini` wrappers; Claude Code via `env.HTTPS_PROXY`/`NO_PROXY` in `~/.claude/settings.json`; VS Code via its settings (JSONC parsed with a real parser, comments preserved).
- JSON/JSONC writes: parse → modify one key → atomic write with backup; race protection against the owning app: read-modify-write with mtime/hash check and retry; never write if the file changed between read and rename.
- Every change journaled (§8); disabling the feature, exit and uninstall revert exactly our keys.
- Codex `respect_system_proxy` detected and explained (it makes Codex ignore `HTTPS_PROXY`).

---

## 10. UI and CLI

Screens for mockups (stage 0b, with frontend-design, own visual identity): tray popup (OK / connecting / VPN down with frozen apps and countdown / protection only), panel (overview, profiles & subscriptions, apps, allowed, networks, checks, statistics & history, settings, about), first run (paste link → connect → choose apps). Russian and English from day one; light/dark following the system. Tray icon shows country code + state colour. Screen-share mode hides IP.

CLI exit codes [CALL]: `check` 0 ok / 1 not allowed / 2 unknown (couldn't check) / 3 daemon not running; `run`: exit code of the command, 69 if the address is not allowed at start, 75 if the address did not return in time (same as `kordon run` for script compatibility).

---

## 11. Updates, security, release

- `SHA256SUMS` signed with Ed25519; app, helper and install scripts all verify the signature. Key generated by Artur from our instructions, stored in CI secrets.
- Updater: pre-flight writability check; download to staging; new version waits for the old one (≤ 2 min), launched detached; starts the supervisor; staging cleaned on next start; fallback to latest available release on 404 (K32–K39).
- Every release checked on VirusTotal before publishing; results in release notes.
- CI ready for SignPath (Windows) and Apple Developer ID later; unsigned builds explain "More info → Run anyway" and the macOS privacy setting; Keychain prompts after update explained in UX.

---

## 12. Budgets [CALL, measured in 0c]
- Idle CPU (all OK, no UI open): < 0.5 % average over 10 min on a 4-core laptop.
- RSS: zatvord ≤ 60 MB; supervisor ≤ 10 MB; hidden UI per framework measured in 0c, target ≤ 120 MB; Xray excluded (depends on config).
- Popup open time from hidden: ≤ 150 ms.

---

## 13. Testing

- TDD for all pure logic: parsers, Happ→Xray translation, header parsing, rule order, legacy noise migration, geo merge, journal/restore, the protection automaton (fake clock/network/process table).
- `go test ./...` has no side effects: temp dirs, random free ports, no system changes.
- `systemtest` build tag for real system changes: Windows Sandbox (`.wsb` + scripts in repo), VM, CI runners.
- Integration in CI: local Xray server (VLESS Reality, Hysteria2 + finalmask) and a real client connection; freeze/close on dummy processes.
- `known-pitfalls.md` items map to test IDs; CI fails if a pitfall marked "must test" has no test.
- Secret scanning in pre-commit and CI.

---

## 14. Repository and process

- Git repo with `brief/` ignored, fixtures copied to `testdata/`. Branch per stage, small commits, ADRs in `docs/decisions/`, `CHANGELOG.md`, `docs/STATUS.md`, `AI-SETUP.md`.
- **GitHub repository [DECIDED]:** public, created at the start of 0c (free Actions minutes incl. macOS). README states "in development". Artur creates the empty repo on GitHub (this environment cannot create repositories); I attach it and push.

---

## 15. Stage 0c prototypes (on Artur's PC, Windows Sandbox)

1. Pinned Xray v26.11.9 runs both Hysteria2 fixtures: legacy as is, and also its migrated form (to prove the migration ready) — also verifiable in CI on Linux.
2. TUN prototype A vs B in Windows Sandbox with the leak/sleep/kill checklist (§4.3).
3. Tray + popup prototype in 2 frameworks (Tauri 2 vs Wails v3 or systray+webview) on Windows, built for macOS in CI for Artur to try.
4. Safe mode and the Windows Sandbox test harness.
5. ADRs for every decision above.
6. Installer format choice (machine-wide, adds the service later; candidates WiX/MSI and Inno Setup) and the one-line install script with Ed25519 verification.

Tools list for Artur to install (admin): Go, Node LTS, Rust (if Tauri), Visual Studio Build Tools (C++), Git, Windows Sandbox feature.

---

## 16. Decisions from the brief review (2026-11-19)
Source: the review document "Zatvor: проверка ТЗ" (all 10 questions answered by Artur, 13 risks closed by recommendation). Where it differs from earlier sections, this section wins.
- **Q2.1 Where development happens:** platform-independent Go logic and CI in the cloud; everything touching Windows via Remote Control on Artur's PC. Section 11 rules apply everywhere.
- **Q2.2 User-Agent:** §5.3.
- **Q2.3 Supervisor gives up:** closes protected apps while frozen; with "Don't close apps" it keeps them paused with a notification; never unfreezes without a fresh check. Satisfies "not frozen until reboot" (K3) without failing open.
- **Q2.4 Per-app network block** is tied to "the privileged service is installed", not to TUN: in system-proxy mode a protected app may only reach loopback to our inbound (WFP on Windows). This also closes proxy-ignoring apps (§7.8). In "protection only" mode (no inbound of ours) the block is applied only while the app's group is not OK: everything except loopback is blocked [CALL, review]. Implementation in stage 3; polite close becomes available wherever the block is active.
- **Q2.5 macOS until Developer ID:** protection by freezing only; TUN via utun with a root launchd daemon from a pkg; per-app network block and polite close after signing (Network Extension needs the entitlement).
- **Q2.6 Happ routing profile** replaces both `routing` and `dns` of the subscription; never touches outbounds, finalmask, policy. `RouteOrder` honoured inside the source. The legacy fixture's routing/dns match `happ-routing.json` exactly: it is the golden test for the translation.
- **Q2.7 Probes:** §7.3. **Q2.8 DNS / time zone:** §7.2.
- **Q2.9 Installation:** machine-wide in Program Files from the first release (one admin prompt); the service is added later by the same installer. Consequence: even "protection only" needs admin once. Updater runs elevated for the replace step only.
- **Q2.10 Geo databases:** both roscomvpn files are bundled unmodified with the source in `NOTICE` (roscomvpn-geoip has no licence file; risk accepted by Artur). If a profile's own `Geoipurl`/`Geositeurl` contain tags missing from the bundled sets, connect first on bundled sets with a temporary "everything through the tunnel" rule, download through the tunnel, then switch the profile on. Direct download only by the user's button. The merged tag set of §6.5 is produced locally at runtime.
- **R3.1** §6.4. **R3.2** `install.ps1` verifies Ed25519 with a compact implementation on `System.Numerics.BigInteger` (PowerShell 5.1 has none); macOS/Linux use `openssl` and refuse with instructions if it lacks Ed25519; checked in 0c.
- **R3.3** Local inbound ports chosen once and persisted, changed only on conflict; when we replace provider inbounds we keep their tags so `inboundTag` rules still match.
- **R3.4** Without TUN there is no per-process routing: developer tools get their own inbound, and a rule on its `inboundTag` sends it through the tunnel.
- **R3.5** The whole profile store is encrypted (keys, salamander passwords, auth), not only subscription URLs. The config goes to Xray via stdin, never to disk.
- **R3.6** Subscription requests are forced through the proxy outbound when connected (the panel's `.ru` domain would otherwise go direct). Without the tunnel there are no automatic retries or background updates, only the button.
- **R3.7** Automatic profile selection/failover only among profiles whose exit country is allowed by the protection rules.
- **R3.8** Processes running elevated can't be frozen by a user process: detect and show "not protected"; from stage 3 the service freezes them.
- **R3.9** Own-process exclusion by an exact list of PID + start time (UI, WebView2 children, daemon, supervisor, xray, helper, `zatvor run` itself), never by name or by subtree; tests for "Zatvor launched under a protected Claude Code" and for `zatvor run` from a protected terminal.
- **R3.10** Default IP-check list: not in `.ru`, HTTPS, at least three independent services; DNS check only an A query for `whoami.akamai.net`, no TXT.
- **R3.11** The TUN system-wide kill switch is off by default and turned on with a warning.
- **R3.12** HWID algorithm (§5.5) is frozen forever in an ADR: changing it would burn users' device slots.
- **R3.13** Synthetic test geo files carry both tag sets. The AWG fixture's H1–H4 = 1..4 are presumably anonymisation placeholders; confirm at stage 6.

- **B1 Before any address is confirmed [DECIDED]:** when the user picks apps to protect (first run or later) and the allow-list for their group is empty, the app proposes a **visible rule "only through the Zatvor tunnel"** (the brief's "only through profile X" variant, bound to whichever Zatvor profile is active), confirmed by the user in the same step. Group is OK while our tunnel is up and the route check passes through it; frozen otherwise. Countries/IPs can be added later. The home IP is never involved. In "protection only" mode (no Zatvor tunnel) this rule is not available, and the first-run asks for a country or IP instead.

### Builder-review calls (2026-11-19)
- **B3 Rule insertion.** "We never rewrite provider routing" means: provider rules keep their content and relative order; we may only **prepend** our own rules (internal, block layers we own, per-app, dev-tools) and never edit, drop or reorder the provider's. Source block rules are not lifted: §6.3 step 2 means *our* block layers (user blocks, per-app blocks); the source's own block rules stay where the source put them. Dev tools use both mechanisms: the dev-tool inbound (R3.4, everything from CLIs/IDEs through the tunnel) and domain rules for AI providers (traffic from the system proxy, e.g. browsers). §6.1's list of additions is extended accordingly; the temporary geo-bootstrap rule (Q2.10) is also a prepended rule, removed after the download.
- **B5 Xray under the helper.** If TUN option A wins, in TUN mode Xray is started by the helper (privileged) with config passed over the helper's API/stdin, supervised by the helper and controlled by zatvord; in proxy mode it stays a child of zatvord. Exclusion list (R3.9) includes it either way. UI and protection still run without admin; only TUN needs the service, which the brief already accepts.
- **B8 Per-user rollback in a machine-wide install.** Journals are per user (`%LOCALAPPDATA%\Zatvor\journal`), reverted on that user's exit/next start. The uninstaller reverts the invoking user's journal immediately and registers a one-time per-user cleanup (Active Setup) that reverts other users' journals at their next logon. The helper's own system changes (TUN, routes, WFP) have a machine journal under ProgramData reverted by the helper/uninstaller.
- **B9 Scope.** This spec plans stages 0c–3 (Windows) in detail. Stages 4 (macOS), 5 (server wizard: Hysteria2 TLS via ACME or pinned self-signed), 6 (AmneziaWG), 7 (extension), 8 (Linux) each get their own short spec before they start; the architecture here already accounts for them.
- **Panel facts check.** §5.3 facts were verified directly in 3x-ui source at tag `v3.10.0` (`bf6eda0`), `internal/sub/controller.go` `ApplyCommonHeaders` and `internal/sub/json_routing.go`; the research file covers main at `5297fd3`.
- **Server Xray version.** Artur's server reports Xray 26.10.10 (not in our list of public releases; possibly a pre-release or panel-bundled build). Client v26.11.9 ↔ server Hysteria2/salamander/finalmask compatibility is checked in the stage 1 CI integration test against a server built from the same version, and finally by Artur's home test.

## 17. Open questions for Artur
None. Visual identity and mockups approved 2026-11-19 (https://claude.ai/artifact/WoaYqZzwiHj8adNRapwGuj).
