# Xray-core research for Zatvor (as of 2026-11-19)

Research method: GitHub REST API and github.com HTML were blocked by the sandbox proxy (HTTP 403, "GitHub access to this repository is not enabled for this session"), so the sources were:
- `git clone` of the repos into `/home/claude/research/src/` (XTLS/Xray-core @ `02cf0f6`, 2026-11-19; XTLS/Xray-docs-next @ `0caf7ea`; XTLS/libXray @ `5a0f7d8`; 2dust/v2rayN @ `9bb74a0`; hiddify/hiddify-core @ `4e5a9a3`; hydraponique/roscomvpn-geosite and roscomvpn-geoip; v2fly/domain-list-community; v2fly/geoip; amnezia-vpn/amneziawg-go @ `08fe3d3`; amnezia-vpn/amneziawg-tools @ `ee0f0a9`)
- WebFetch of the github.com release pages, which worked
- `gh api search/issues`, which worked
- release assets downloaded from `github.com/XTLS/Xray-core/releases/download/...`, which worked

cdn.jsdelivr.net was blocked (CONNECT 403), so the pinned roscomvpn `.dat` files were taken from the git tags instead.

Files produced:
- `bin/<ver>/xray`: official binaries, each SHA-256 checked against its `.dgst` file
- `dat/roscomvpn-pinned/`: the `.dat` files at the tags pinned in happ-routing.json
- `dat/roscomvpn-latest/`: the current `.dat` files
- `translated/legacy-to-finalmask.json` and `translated/legacy-to-finalmask-exp.json`: the migration examples
- `fixture-tags.txt`: every geo tag used in the fixtures

---

## 1. Latest stable release

| Tag | Date | Status | Source |
|---|---|---|---|
| **v26.11.9** | 2026-11-09 05:31 | **Latest** (stable). Commit `676eaf6`, signed. Headline: "XDRIVE transport … Google Drive backend" (#7352) | WebFetch https://github.com/XTLS/Xray-core/releases/tag/v26.11.9 ; `xray version` → `Xray 26.11.9 … 676eaf6 (go1.27.3 linux/amd64)` |
| v26.10.31 | 2026-10-31 | Stable. Emergency release: MPTCP disabled by default because of a Linux kernel MPTCP flaw. v26.10.29 was withdrawn. | WebFetch …/releases/tag/v26.10.31 |
| v26.10.21 | 2026-10-21 | Stable. Notes say "10.22 直接先把该版本设为了 latest". Removed 3 root CAs. A v26.9.8 release was deleted. | WebFetch …/releases/tag/v26.10.21 |
| v26.9.30, v26.9.9, v26.7.28, v26.7.11, v26.6.27, v26.6.22, v26.6.1, v26.5.9, v26.4.25, v26.4.17, v26.4.15, v26.4.13, v26.3.23, v26.2.4, v26.2.2, v26.1.31 | — | **Pre-release** | WebFetch https://github.com/XTLS/Xray-core/releases and ?page=2 |
| v26.3.27, v26.2.6 | — | Not pre-release (stable) | same |

Other release facts:
- There are no releases on main after v26.11.9. Main has 24 more commits (`git log v26.11.9..HEAD`). One of them, #7281 (2026-11-18), changes freedom: `finalRules`→`firewall`, Blackhole is merged into freedom, and `proxyProtocol` becomes a finalmask. It is not in any release yet. Expect config breakage in the next release.
- Tags of the form `v1.YYMMDD.0` are Go-module mirrors of the stable releases. For example, `v1.260327.0` = `v26.3.27`, and `v1.260206.0` = `v26.2.6` (`git tag --points-at`).
- Cadence: since April 2026 most releases are pre-releases. The stable ones are v26.10.21, v26.10.31 and v26.11.9. **Zatvor should pin to stable tags only.**

## 2. finalmask

**History** (`git log --grep finalmask`; first tag from `git tag --contains`):

| Commit | Change | First tag |
|---|---|---|
| `92ada2d` (#5508) | Hysteria v2 outbound and transport, plus "Salamander udpmask" | v26.1.13 |
| `c180c59` (#5560) | "Finalmask: Add XDNS, header-*, mkcp-*" (first commit named finalmask) | v26.1.31 (pre) |
| `888c0d2` (#5643) | finalmask UDP for WireGuard and Shadowsocks | v26.2.x |
| `a204873` (#5657) | **`noise` (UDP)**, `fragment` (TCP), header-custom, support for dialer-proxy and XHTTP/3 | v26.3.23 (pre) |
| `0321cdd` (#5772) | `finalmask.quicParams` (`congestion`, brutal, udpHop) | v26.3.23 |
| `14524cc` (#5850) | `randRange` for noise | v26.3.27 |
| `09002ab` (#6188) | better `reset` | v26.6.1 |
| `e51b3c3` (#6862) | **noise `"type": "exp"`** (the form the finalmask fixture uses) | **v26.9.30 (pre)**; first stable is v26.10.21 |
| `206a24a` (#7124) | Lua `script` for noise | v26.10.21 |

**Schema.** Source: `infra/conf/transport_finalmask.go` and https://xtls.github.io/en/config/transports/finalmask.html (docs repo `docs/en/config/transports/finalmask.md`). The object lives at `streamSettings.finalmask`:

```
{ "tcp": [ {type, settings} ], "udp": [ {type, settings} ], "quicParams": {...} }
```

- UDP mask types: `filter`, `header-custom`, `mkcp-legacy`, `noise`, `salamander`, `sudoku`, `xdns`, `xicmp`, `realm`, `udphop`.
- TCP mask types: `filter`, `header-custom`, `proxy-protocol`, `fragment`, `sudoku`, `xmc`.
- **Order.** The docs say "The first item in the array is the innermost camouflage layer". In code, `NewFinalMask` calls `slices.Reverse(udpMasks)` and then wraps the socket, so the **last** item is the one nearest the wire. For the fixture this means `[salamander, noise]` sends the noise packets raw, unobfuscated, so they look like a real DNS reply. Salamander only covers the QUIC packets. Putting `noise` first would salamander-encrypt the fake DNS packet. (Source: `transport/internet/finalmask/finalmask.go`.)

**`salamander`** (`transport/internet/finalmask/salamander/salamander.go`):
- This is the Hysteria2 obfuscator. Each packet is `[8-byte random salt][payload XOR BLAKE2b-256(PSK‖salt)]`, and the PSK must be at least 4 bytes.
- The optional `packetSize: "min-max"` (≤2048) switches to "Gecko" fragmentation and padding of QUIC long-header packets (#6198, Hysteria v2.9.2).

**`noise`** (`transport/internet/finalmask/noise/conn.go`):
- Settings are `reset` (Int32Range, in seconds) plus a `noise[]` list. Each item has `rand`, `randRange`, `type` (`array|str|hex|base64|exp`), `packet` and `delay` (ms). Alternatively `script` (Lua) can be used.
- On the **first `WriteTo` to each destination address**, the conn sends every noise item in order, sleeping `delay` ms after each one, and only then sends the real packet. With `reset: 0` this happens once per address per socket. With `reset > 0` it repeats after that many seconds.
- The `exp` grammar is `<b hex>`, `<r N|A-B>` (random bytes), `<rc>` (letters), `<rd>` (digits), `<t>` (unix time, 4 bytes BE), `<c>` (counter, 4 bytes), `<n>` (8 random bytes). The same grammar as the AmneziaWG `I1` syntax.

**Fixture's fake DNS response** (decoded by hand from the hex): `<r 2>` = random DNS transaction ID, then `8580` = flags QR=1 (response), AA=1, RD=1, RA=1, RCODE=0. QD=1, AN=1. Question `example.com` A IN. Answer pointer `c00c` A IN, TTL 0x105a = 4186 s, RDLENGTH 4, address `5d b8 d7 0e` = 93.184.215.14.

So, before the first QUIC Initial, the client sends one UDP datagram that looks like a DNS A-record **response** to the server's ip:port, waits 5–10 ms, and then starts QUIC. The intent is to confuse a stateful DPI classifier on the first packet of the flow. Whether this defeats any particular DPI is UNVERIFIED.

**Supported outbounds and transports.** `FinalMask.DialUDP` and `DialTCP` are used by:
- hysteria (`transport/internet/hysteria/dialer.go:123`)
- kcp, splithttp (XHTTP incl. h3), masque, udp, tcp, websocket, httpupgrade, grpc, xdrive
- the wireguard proxy (`grep -rln FinalMask.DialUDP`)

The **Hysteria v2 outbound supports finalmask.udp, including noise.** Masks go through `DialSystem`, which honours `sockopt.dialerProxy` (`transport/internet/memory_settings.go`).

## 3. Legacy freedom `noises` + `dialerProxy`

**Status: deprecated, not removed**, both in the latest release and on main.
- Deprecated by `613d814`, 2026-10-29, "Direct/Freedom outbound: Warn about migrating `fragment` and `noises` to Finalmask (#7242)". First tag: **v26.10.31**.
- `infra/conf/freedom.go` on main (`02cf0f6`) still parses `noises` and calls `errors.PrintDeprecatedFeatureWarning`. `fragment` gets the same treatment.
- The older single `noise` object is already removed (`PrintRemovedFeatureError("noise = { ... }", ...)`).
- No removal commit exists yet. Removal date: UNVERIFIED (the warning only says "will be removed soon").
- Earlier history:
  - freedom noises added in `002d08b`, v24.9.7
  - base64 and multi-packet in v24.9.16
  - `hex` type in `ca50c9c`, v25.1.30
  - `applyTo` in `cde6e33`, v25.8.29

**How the legacy path works** (`proxy/freedom/freedom.go` `NoisePacketWriter`). The hysteria transport dials UDP through the `dns-noise` freedom outbound. On the **first write** of that freedom UDP session, freedom sends each noise (sleeping `delay` after each), unless the redirect port is 53. Then it forwards the QUIC packet.

**Mechanical translation** (implemented with jq, giving `translated/legacy-to-finalmask.json`):
1. Let `D` = the outbound named in `X.streamSettings.sockopt.dialerProxy`, where `D.protocol=="freedom"` and `D.settings.noises` is non-empty.
2. For each noise `n` in `D.settings.noises`, emit `{type, packet, delay}` according to this table:

   | legacy `type` | finalmask item |
   |---|---|
   | `hex` | `{"type":"hex","packet":<same, whitespace stripped>,"delay":d}` |
   | `str` | `{"type":"str","packet":p,"delay":d}` |
   | `base64` | `{"type":"base64","packet":<re-encoded std padded>,"delay":d}` |
   | `rand` (packet `"a-b"`) | `{"rand":"a-b","delay":d}` with no `packet` |

3. Append `{"type":"noise","settings":{"reset":0,"noise":[...]}}` as the **last** element of `X.streamSettings.finalmask.udp`, after salamander or any other obfuscator.
4. Delete `X.streamSettings.sockopt.dialerProxy`, and delete `sockopt` if it is now empty.
5. Delete `D` if no other outbound or rule references its tag.
6. Delete empty `"noises": []` from other freedom outbounds. The fixture's `direct` outbound has one, and it triggers the second deprecation warning.

**Edge cases:**
- **base64:** legacy accepts URL-safe or unpadded input (it normalises and uses `RawURLEncoding`). finalmask uses strict `base64.StdEncoding`, so decode and re-encode.
- **hex:** legacy calls `TrimSpace`; finalmask does not. Type names are case-insensitive in finalmask but case-sensitive in legacy.
- **`applyTo` (ip/ipv4/ipv6)** has **no finalmask equivalent** (`NoiseItem` has no such field). Translating it is lossy, so warn or drop.
- **Port 53:** legacy skips noise when freedom's redirect port is 53; finalmask doesn't.
- **Scope changes:**
  - legacy: once per freedom UDP session
  - finalmask: once per destination address per socket
  - finalmask with `reset > 0`: repeated
  - with Hysteria `udphop`, finalmask sends noise to each new port. UNVERIFIED in a live run.
- **What `D` did besides noise is lost when it is removed:** its own `domainStrategy`, `redirect`, `sockopt` (mark/interface), or a chained `dialerProxy`. Copy those into `X.streamSettings.sockopt` where an equivalent exists, or keep `D` without `noises`.
- **Shared `D`:** if `D` serves several outbounds, add the mask to each of them.
- **Exact replay vs random ID:** the legacy fixture's fixed transaction ID `a3c1` is replayed byte-for-byte. The finalmask fixture's `<r 2>` (randomised ID) is a semantic improvement, not a mechanical translation. It needs ≥ v26.9.30, and v26.10.21 is the first stable.
- **Ambiguous configs:** if `finalmask.udp` already contains a `noise` entry, or the noise freedom is itself a `dialerProxy` target chain, refuse to auto-translate.

**Test results** (official binaries, `XRAY_LOCATION_ASSET` set, `xray run -test -c`; full logs reproduced below):

| Binary (SHA-256 = .dgst) | dat set | finalmask fixture | legacy fixture |
|---|---|---|---|
| v26.11.9 `b651f7e1…5f82` | roscomvpn pinned and latest | **FAIL** exit 23: `illegal ip rule: geoip:ru > failed to check code RU from geoip.dat > EOF` | **OK**, with 2× deprecation warning |
| v26.11.9 | Xray bundled (XTLS/Xray-rules-dat) | **OK** | **FAIL** exit 23: `illegal domain rule: geosite:win-spy > failed to check code WIN-SPY from geosite.dat > EOF` |
| v26.10.31 `e4bd9119…b214` | bundled / roscom-pinned | OK | OK + 2× warning |
| v26.9.9 `1eb9175d…b7df` (pre) | bundled / roscom-pinned | **FAIL**: `failed to build mask with type noise > infra/conf: unknown type` (no `exp`) | OK, no warning |
| v26.3.27, v26.2.6 | roscom-pinned | — | OK (see the caveat after the logs) |
| translated `legacy-to-finalmask.json` (hex) | roscom-pinned | OK on v26.11.9, v26.10.31, v26.9.9, with no warning | |
| translated `legacy-to-finalmask-exp.json` | roscom-pinned | OK on v26.11.9 and v26.10.31; v26.9.9 FAILS (`unknown type`) | |

Verbatim output for v26.11.9 with roscomvpn-pinned:

```
Xray 26.11.9 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3 linux/amd64)
A unified platform for anti-censorship.
[Info] infra/conf/serial: Reading config: &{Name:/mnt/project-files/zatvor/brief/fixtures/xray-hy2-dns-finalmask.json Format:json}
Failed to start: main: failed to load config files: [/mnt/project-files/zatvor/brief/fixtures/xray-hy2-dns-finalmask.json] > infra/conf: failed to build routing configuration > infra/conf: invalid field rule > common/geodata: illegal ip rule: geoip:ru > common/geodata: failed to check code RU from geoip.dat > EOF
exit=23
---
[Info] infra/conf/serial: Reading config: &{Name:/mnt/project-files/zatvor/brief/fixtures/xray-hy2-dns-legacy.json Format:json}
[Warning] common/errors: This feature "noises" in Freedom is deprecated, will be removed soon and being migrated to "noise" in "streamSettings.finalmask.udp". Please update your config(s) according to release note and documentation before removal.
[Warning] common/errors: (same, second time — from the "direct" outbound's empty "noises": [])
Configuration OK.
exit=0
```

**Caveat: `-test` is a weak validator.** Unknown JSON keys are silently ignored. A config with `"totallyUnknownKey"`, unknown `settings` keys and `finalmask.quicParams.nonsense` prints `Configuration OK.` on both v26.11.9 and v26.2.6 (my own test file). So "OK on v26.2.6" does **not** prove that `quicParams` was honoured, because `quicParams` arrived only in v26.3.23. Zatvor needs its own schema validation per Xray version.

**Minimum versions:**
- legacy fixture as intended (needs `quicParams`): v26.3.23. The oldest stable is v26.3.27.
- finalmask fixture: v26.9.30. The oldest stable is v26.10.21.

## 4. Geo databases in the tests

See §7. Neither dat set alone satisfies both fixtures. The finalmask fixture uses `geoip:ru`, which roscomvpn geoip lacks. The legacy fixture uses roscomvpn-only tags.

## 5. Xray's own TUN inbound

**Timeline** (`git log -- proxy/tun`, `git tag --contains`):

| What | Commit | First tag |
|---|---|---|
| Windows (Wintun) & Linux incl. Android; `wintun.dll` bundled in Windows zips | `39ba1f7` (#5464), `394e117` (#5501) | v26.1.13 |
| macOS (utun) | `6d6c045` (#5559) | v26.1.18 |
| iOS | `c3af657` | v26.1.31 |
| Windows `gateway`/`dns`/`autoSystemRoutingTable`/`autoOutboundsInterface` | `806b8dc` (#5887) | v26.4.13 |
| FreeBSD | `6780045` | v26.4.15 |
| Windows `autoSystemWfpBlockLeak` (WFP DNS / leak block); Linux `autoSystemDnsToGateway` | `1f30491` (#6853) | v26.9.30 |
| Own lightweight TCP/IP stack replacing gVisor | `70ef97d` (#7130) | v26.10.21 |

**Maturity and caveats:**
- The README (`proxy/tun/README.md`) says: "If you are not sure what this is and do you need it or not - you don't … intended to be configured by network professionals".
- Only ICMP echo is supported, with fake local replies.
- TCP to dead hosts gets a SYN-ACK and then an RST later.
- macOS does not set system DNS.
- Linux DNS takeover requires systemd-resolved ≥240, and Xray refuses to start otherwise.
- `SIGKILL` leaves `resolvectl` state behind.
- The stack was swapped from gVisor to Xray's own stack only one month ago (v26.10.21).
- The Windows IPv6/DNS paths were still being fixed in November: #7325, #7332, #7341, #7387 ("TUN fails to start on Windows 11 24H2 when DisabledComponents=0x28").

Open issues (`gh api search/issues`):
- #7160: if the interface is missing, a socket can be left unbound and loop back into the TUN
- #6535: Windows process-name sniffing fails for elevated processes
- #5845: Moonlight streaming fails

Assessment: usable, and v2rayN ships it (see below), but it is young and changing fast.

**v2rayN** (`v2rayN/ServiceLib`, HEAD `9bb74a0`, 2026-11-19) has two modes:
- **Default, `TunModeItem.EnableLegacyProtect = true`** (`Models/Configs/ConfigItems.cs:139`): for a non-sing-box node with TUN on, `GetPreSocksItem` creates a **sing-box "pre" core** (`configPre.json`). This sing-box TUN frontend forwards into Xray's local SOCKS port (`Handler/Builder/CoreConfigContextBuilder.cs:531-560`, `Manager/CoreManager.cs:205-235`).
- **Native mode, with legacy protect off:** v2rayN puts an **Xray `tun` inbound** directly into the Xray config (`Services/CoreConfig/V2ray/V2rayInboundService.cs:56-141`):
  - name `xray_tun`, or `utunNN` on macOS
  - `gateway`, `autoSystemRoutingTable` (0.0.0.0/0 and optionally ::/0)
  - `autoOutboundsInterface`
  - `autoSystemWfpBlockLeak: ["dns","misconfigtun"]` on Windows when StrictRoute is set
  - `autoSystemDnsToGateway: true` on Linux
  - sniffing `routeOnly: true`
- **No tun2socks in either mode.** Cores run as child processes, with sudo for TUN on non-Windows (`CoreManager.ShouldRunAsSudo`).
- Recent v2rayN issues about native Xray TUN: #10583 (macOS route exclusions), #10523 (leftover root sing-box), #10518 (routing loops in dual-core).

## 6. Library vs separate process

**What other clients do:**
- **v2rayN:** separate processes (`xray.exe`, `sing-box.exe`, `mihomo.exe`), launched by `ProcessService` (`CoreManager.RunProcess`). The CI smoke test runs `xray\xray.exe -version` (`.github/workflows/build-WebAPI.yml:271`).
- **Hiddify (hiddify-core):** a Go library that embeds sing-box in-process. `go.mod` has `sagernet/sing-box v1.13.0`, `xtls/libxray v1.261031.0` and `xtls/xray-core v1.260327.1-0.20261109052709-676eaf66f8f0` (= v26.11.9).
  - Today libxray is used for share-link conversion (`v2/config/xray_json.go`, `share.ConvertXrayJsonToShareLinks`).
  - The sing-box "xray" outbound patch is commented out (`v2/config/outbound.go:205`).
  - HISTORY.md shows earlier in-process Xray ("Suppoer full xray configs and update xray to 25.3.6").
  - Whether Xray-in-process is still active in the shipping runtime is UNVERIFIED.
  - License: GPL-3 with extra §7 "NonCommercial" and "no store listing" terms (`LICENSE.md`).
- **libXray** (XTLS, MIT):
  - a wrapper for gomobile and cgo, used mainly by mobile clients (README: Android/Apple builds and `cgo_bridge`)
  - pins Xray v26.11.9
  - "does not guarantee API stability"
  - "only compatible with the latest release of Xray-core"
  - there is also a `desktop_bin` command wrapper

**Crash isolation** (analysis, not measured):
- An unrecovered panic in any Xray goroutine terminates the whole Go process. A Go runtime also cannot be restarted in-process.
- Xray panics do happen in 2026, e.g.:
  - #7369 (XtlsPadding slice bounds)
  - #6647–6650 (Reality nil panic)
  - #6729 ("finalmask fragment: negative `length` passes -test and panics the whole process on first connection")
  - #6653 (Hysteria outbound without address panics on load)
- Embedding Xray in the Zatvor daemon means that daemon dies too. It holds TUN, routes, WFP filters and the kill-switch, and the system-DNS state is not reverted on crash (README: not reverted on SIGKILL).
- A separate `xray` child process, supervised by the daemon, contains the blast radius. The daemon can then restore routes and DNS and restart Xray.
- The cost is IPC: use the gRPC API (`api` section / `xray api`) for stats and hot-swapping outbounds. v2rayN users ask for exactly this hot-swap in #10538.
- Using the official signed binaries (verifiable against `.dgst`) also makes reproducibility and updates easier.

**Licensing:**
- Xray-core is MPL-2.0 (`LICENSE`). No source file carries the Exhibit B "Incompatible With Secondary Licenses" notice (`grep -rl` found none), so MPL §3.3 lets Zatvor (GPL-3.0) combine with it, either linked into a "Larger Work" or as a separate binary.
- Duties: provide the source of the MPL files (including any modifications) under MPL, and keep the notices.
- Shipping the unmodified official binary as a separate process is the simplest case.
- Bundled dependencies: Wintun (`wintun.dll` in the Windows zips) has its own prebuilt-binary licence, UNVERIFIED. gopher-lua is MIT (UNVERIFIED licence, dependency seen in `go.mod`).
- libXray is MIT, which is GPL-3-compatible.

## 7. roscomvpn geo databases

Repositories:
- https://github.com/hydraponique/roscomvpn-geosite, a fork with v2fly domain-list-community history going back to 2018
- https://github.com/hydraponique/roscomvpn-geoip
- sibling: roscomvpn-routing (Happ/Mihomo/Incy)

The jsDelivr URLs pinned in happ-routing.json (`roscomvpn-geoip@202610260848`, `roscomvpn-geosite@202610120948`) are git **tags**. `git ls-remote` gives:
- geosite tag 202610120948 → `bb0e6b3`
- geoip tag 202610260848 → `152ad51`

The pinned `.dat` SHA-256 values match the repos' own `release/*.dat.sha256` files at those tags: geoip `3273bef3…a6bd`, geosite `95f15c43…f001`.

**Cadence** (`git ls-remote --tags`):
- **geoip:** 416 tags, released **daily** at about 10:00–11:30 UTC. The latest is `202611191131`.
- **geosite:** 114 tags, released irregularly. The latest is `202610120948` (2026-10-12), which is exactly what the fixture pins. Before that, tags cluster in March–April and October 2026.
- Tag format: `YYYYMMDDhhmm`. There are also `test_*` tags.

**Categories:**
- geoip has only `direct`, `whitelist` and `private` (README "Три категории"; `release/text/`).
- geosite (`data/`, 24 files) has: apple, category-ads, category-geoblock-ru, category-ru, epicgames, escapefromtarkov, faceit, github, google-deepmind, google-play, microsoft, origin, pinterest, private, push, riot, steam, telegram, torrent, twitch, twitch-ads, whitelist, win-spy, youtube.

**Fixture tags** (from `grep -oh` over the JSON fixtures):
- `geoip:` direct, private, ru
- `geosite:` apple, category-ads, category-ru, epicgames, escapefromtarkov, faceit, github, google-play, microsoft, pinterest, private, riot, steam, telegram, torrent, twitch, twitch-ads, whitelist, win-spy, youtube

Each tag was checked with `xray run -test` on a one-rule config per tag:

| dat set | missing tags |
|---|---|
| Xray-bundled (XTLS/Xray-rules-dat, per `.github/workflows/scheduled-assets-update.yml`) | `geoip:direct`, `geosite:torrent`, `geosite:twitch-ads`, `geosite:whitelist`, `geosite:win-spy` |
| roscomvpn pinned / latest | `geoip:ru` only |
| v2fly domain-list-community `data/` (source files) | `torrent`, `twitch-ads`, `whitelist`, `win-spy`. All the other geosite tags exist. |

`geoip:direct` and `geoip:whitelist` are roscomvpn-specific. v2fly/geoip produces country codes plus `private`; the full list is UNVERIFIED.

**Consequence:** the finalmask fixture expects a standard geoip (`geoip:ru`), and the legacy fixture and Happ profile expect roscomvpn. Zatvor must treat the dat set as part of the profile, and should map `geoip:ru` to `geoip:direct` or ship both sets.

## 8. AmneziaWG

amneziawg-go (module `github.com/amnezia-vpn/amneziawg-go/v3`) is **MIT**: LICENSE "Copyright (C) 2017-2025 WireGuard LLC … Permission is hereby granted…", and all SPDX headers say MIT. amneziawg-tools (`awg`, `awg-quick`) is `GPL-2.0 OR MIT` for its C sources and ships a GPL-2 COPYING. It runs as a separate tool.

**Versions** (commit titles, `git log`; first tag from `git tag --contains`):

| Feature | Commit / tag | Date |
|---|---|---|
| "AmneziaWG v1.5": `I1`–`I5` custom signature packets (`<b>`,`<r>`,`<rd>`,`<rc>`,`<t>`), S3/S4 | `c207898`, v0.2.13 | 2025-07-07 |
| "awg 2.0": H1–H4 ranges | `f654220`, v0.2.15 | 2025-09-01 |
| "amneziawg 3.0": `HeaderProtectionKey` (needs S1–S4 ≥ 12), `ContentPaddingAddition`, timing **ranges** `RekeyAfterTime`/`RekeyTimeout`/`RejectAfterTime`/`KeepaliveTimeout`/`MaxHandshakeAttempts`, PersistentKeepalive range | `457d920`, v3.0.0 | 2026-07-24 |
| "awg 3.1": `RandomTrailers`, `DisableCookies` | `1f50ad7`, v3.1.20260812 | 2026-08-12 |
| latest tag | v3.1.20261116 (the tagged commit is dated 2026-09-03) | — |

The README section headers read "[AWG 3+]" and "[AWG 3.1+]".

**The fixture `amneziawg.conf` needs AWG 3.1**, i.e. amneziawg-go ≥ v3.1.20260812, because of RandomTrailers and DisableCookies. HeaderProtectionKey and the timing ranges alone would need ≥ v3.0.0.

**Naming:**
- "AmneziaWG 1.5 / 2.0 / 3.0 / 3.1" is the upstream naming in commit titles and the README.
- "2.0" means H-ranges; I1–I5 came in 1.5.
- Calling this config "2.0" would be wrong. It is **3.1**.

**Fixture edge cases:**
- `RandomTrailers = on` is accepted by `awg`/`awg-quick`, whose `parse_bool` takes "on" (`src/config.c:426`).
- The amneziawg-go UAPI uses `strconv.ParseBool` (`device/uapi.go`, `random_trailers`), which rejects "on". If Zatvor drives the UAPI directly, it must map on/off to true/false.
- S1–S4 = 12 meets the header-protection minimum. The README also recommends equal S1–S4 when using RandomTrailers.
- RandomTrailers is "server-side" (it must match on both ends).
