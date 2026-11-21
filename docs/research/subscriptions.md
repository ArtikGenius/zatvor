# Zatvor research: subscriptions, panels, Happ conventions, share links

Research date 2026-11-19. Read-only. No request was sent to any real subscription URL.

## Sources (pinned)

Repos were shallow-cloned into `/home/claude/research/src/`. All file paths below are relative to each clone.

| Repo | Clone dir | HEAD commit | Commit date |
|---|---|---|---|
| MHSanaei/3x-ui (main) | `MHSanaei_3x-ui` | `5297fd3c8495d80fab97ae511553fa545c69e913` | 2026-11-13 |
| Gozargah/Marzban (master, the only branch) | `Gozargah_Marzban` | `7f396db3e703d71a28060bc9ce4a532ec64cb1f4` | 2025-01-09 |
| remnawave/backend | `remnawave_backend` | `6853666801d6d1925b6aec55824106100485777a` | 2026-11-14 |
| remnawave/subscription-page | `remnawave_subscription-page` | `9045728e6e197a2581c6da40f4f92b170a8bad53` | 2026-10-22 |
| marzneshin/marzneshin | `marzneshin_marzneshin` | `d3b25e23c4977c63eacc6aca591e0cdf0c2bcd68` | 2025-10-03 |
| 2dust/v2rayN | `2dust_v2rayN` | `9bb74a097b50e76ed490444bf56b20bbea6746f7` | 2026-11-19 |
| clash-verge-rev/clash-verge-rev (sparse clone) | `cvr` | `3f7af9ab022d04ebc45d96b56b58cd67e90fe90d` | n/a |

Happ documentation index: https://www.happ.su/main/llms.txt. Direct `curl` to happ.su was refused by the sandbox proxy with `CONNECT tunnel failed, response 403`. I read the Happ pages through WebFetch, which returns summaries, so the Happ statements below are paraphrases, not verbatim quotes.

---

## 1. 3x-ui (main)

The subscription code now lives in `internal/sub/`, not in a top-level `sub/` package.

### Routes
Source: `internal/sub/controller.go`, functions `defaultSUBControllerConfig` and `initRouter`.

- **`/sub/:subid`** is always registered. It also serves `/sub/:subid/hwid-status`, which returns a read-only JSON count of device slots.
- **`/json/:subid`** is registered only when `subJsonEnable` is true. That setting defaults to `"false"` (`internal/web/service/setting.go:105`).
- **`/clash/:subid`** is registered when Clash output is enabled. Two aliases come with it: `/mihomo/:subid` and `/clash-legacy/:subid`.
- All three path prefixes are configurable. The defaults are `"/sub/"`, `"/json/"` and `"/clash/"`.
- Every route answers both GET and HEAD.
- `setNoCacheHeaders` adds `Cache-Control: no-cache, no-store, must-revalidate`, `Pragma: no-cache` and `Expires: 0` to every response.

### How `/sub/:subid` picks a format
Source: `controller.go`, function `subs`. The checks run in this order:

1. If the query has `?format=info`, the server returns a JSON info object.
2. If `Accept` contains `text/html`, or the query has `?html=1` or `?view=html`, the server returns the HTML subscription page.
3. `enforceHwid` runs next (see the HWID section below).
4. If `subClashAutoDetect` is on, Clash is enabled, and the User-Agent matches `subClashUserAgentRegex`, the server returns Clash/Mihomo YAML. The default regex is `` (?i)(clash|mihomo) `` (`setting.go:38`).
5. If `subJsonAutoDetect` is on, JSON is enabled, and the User-Agent matches `subJsonUserAgentRegex`, the server returns full Xray JSON with `Content-Type: application/json`. The default regex is empty (`setting.go:40`), so JSON is never auto-served unless the admin sets a pattern.
6. Otherwise the server returns one share link per line. The body is base64 (StdEncoding) when `subEncrypt` is true, which is the default. When it is false, the body is plain text.

Happ handling inside step 6:
- Happ is detected with `` (?i)\bhapp\b `` (`internal/sub/happ.go:10`).
- For Happ and INCY clients, the link remark keeps the server description. For other clients it is stripped (`subReq.omitServerDescription`).

**Conclusion:** with default settings, Happ gets base64 links on 3x-ui. It gets Xray JSON only from `/json/`, or from `/sub/` when the admin sets the JSON User-Agent regex.

### Response headers
Source: `controller.go`, function `ApplyCommonHeaders`, around lines 946-1006.

| Header | Value |
|---|---|
| `Subscription-Userinfo` | `upload=%d; download=%d; total=%d; expire=%d`. `expire` is in seconds (`internal/sub/calendar_expire.go:20`). |
| `Profile-Update-Interval` | Hours. Default `"12"`. |
| `Profile-Title` | `base64:` followed by StdEncoding of the UTF-8 title. Always sent base64-prefixed. |
| `Support-Url` | The configured URL. |
| `Profile-Web-Page-Url` | The configured URL. |
| `Announce` | `base64:` followed by StdEncoding of the text. |
| `Routing-Enable` | `true`, or `0` for a managed Happ client. |
| `Routing` | A `happ://routing/onadd/<base64>` deep link. |
| `Hide-Settings` | `1` or `0`. |

**Happ-only headers.** These are sent only when `subHappAutoDetect` is on and the User-Agent matches Happ (`internal/sub/happ.go`, function `ApplyHappHeaders`):
- `ProviderID`, `New-Url`, `Fallback-Url`
- `Sub-Info-Color` / `-Text` / `-Button-Text` / `-Button-Link`
- `Sub-Expire`, `Sub-Expire-Button-Link`, `Notification-Subs-Expire`
- `No-Limit-Enabled`, `Subscription-Always-Hwid-Enable`
- `Tun-Mode`, `Tun-Type`, `Exclude-Routes`, `Exclude-Apns-Enable`
- `Color-Profile`, `Ping-Type`
- `Subscription-Autoconnect`, `Subscription-Autoconnect-Type`
- `Socks-Auth-Mode`, `Http-Auth-Mode`
- `Per-App-Proxy-Mode`, `Per-App-Proxy-List`

**Routing documents.** `internal/sub/remote_routing.go:374-411` (`normalizeHappRouting`) accepts either raw routing JSON or a `happ://routing/(onadd|add)/<b64>` link.
- Raw JSON is compacted with Go `json.Marshal` and wrapped as `happ://routing/onadd/` plus StdEncoding base64. Go's `json.Marshal` on a map sorts keys alphabetically, so key order is not preserved.
- For decoding, it tries Std, RawStd and URL base64 variants (`decodeRoutingBase64`).

**Settings keys and defaults** (`internal/web/service/setting.go:84-170`):
- `subEnable=true`
- `subJsonEnable=false`
- `subJsonAutoDetect=false`
- `subJsonAlwaysArray=false`
- `subClashAutoDetect=false`
- `subEnableRouting=false`
- `subHappAutoDetect=false`
- `subHappLocalProxyAuth=auto`
- The `subHapp*` and `subIncy*` families.

**Happ crypt5 links.** `internal/web/service/happ_crypto.go` generates `happ://crypt5/` links locally.
- It uses RSA-4096 PKCS#1 v1.5 to wrap a key, plus ChaCha20-Poly1305 and character-swap framing with the marker `vdfzfoff`.
- The comment says the public key was taken from the third-party reverse-engineering repo `Omegaplexx/hpwnr`.

### HWID / device limit: present in main
- **Model.** `internal/database/model/model.go:1014` defines `LimitHwid int` (`json:"limitHwid"`, column `limit_hwid`, default 0). Line 1130 defines `ClientHwid`, with table `client_hwids` (line 1142). Its columns include `sub_id`, `hwid_hash` (SHA, size 64, unique per sub), `last_seen`, `user_agent`, `device_os`, `os_version` and `device_model`.
- **Request headers read.** `controller.go:717-735` (`enforceHwid`) reads `X-HWID`, `User-Agent`, `X-Device-OS`, `X-Ver-OS` and `X-Device-Model`.
- **Logic.** `internal/web/service/client_hwid.go:116` (`EnforceHwidForSubID`); minimum HWID length is 6 (`minHwidLength = 6`, line 47).
  - With limit 0, all requests are allowed. Devices are still tracked when an HWID is present.
  - With limit > 0 and the HWID missing or too short, the result is `NotSupported`.
  - With a new device over the limit, the request is rejected.
  - **When a request is not allowed, the server replies HTTP 404 with no body.**
- **Response headers** (`controller.go:737-750`): `X-Hwid-Active`, `X-Hwid-Not-Supported`, `X-Hwid-Limit` and `X-Hwid-Max-Devices-Reached`, each set to `true`.
- **When it landed.** It was merged to main in PR #5802, "feat(sub): add per-client subscription HWID limits", on 2026-08-15 (https://github.com/MHSanaei/3x-ui/pull/5802).
  - Follow-ups: #6265, #6380 (the hwid-status endpoint), and #6434 ("Happ client integration, routing presets", merged 2026-09-10).
  - Open PRs: #7133 (per-subscription HWID bans) and #7106 (replace configs rejected by HWID limits).
  - Found with `gh api -X GET search/issues -f q='repo:MHSanaei/3x-ui hwid limit is:pr is:merged'`.
- **No fork is needed** to get HWID support; it is in main.

---

## 2. Marzban, Remnawave, Marzneshin

### Marzban (`Gozargah/Marzban`, master, 2025-01-09)
Source: `app/routers/subscription.py`.

**Paths.** `/{XRAY_SUBSCRIPTION_PATH}/{token}`. The path defaults to `sub` (`config.py:36`).
- Sub-routes: `/info`, `/usage`, and `/{client_type}`.
- `client_type` must match `sing-box|clash-meta|clash|outline|v2ray|v2ray-json`.
- If `Accept` contains `text/html`, the server returns the HTML page.

**User-Agent rules,** checked in order with `re.match`, so each pattern is anchored at the start:

| User-Agent pattern | Response |
|---|---|
| `^([Cc]lash-verge\|[Cc]lash[-\.]?[Mm]eta\|[Ff][Ll][Cc]lash\|[Mm]ihomo)` | clash-meta YAML |
| `^([Cc]lash\|[Ss]tash)` | clash YAML |
| `^(SFA\|SFI\|SFM\|SFT\|[Kk]aring\|[Hh]iddify[Nn]ext)` | sing-box JSON |
| `^(SS\|SSR\|SSD\|SSS\|Outline\|Shadowsocks\|SSconf)` | outline JSON |
| `^v2rayN/`, `^v2rayNG/`, `^[Ss]treisand` | Xray JSON only behind version gates and the `USE_CUSTOM_JSON_*` flags |
| `^Happ/(\d+\.\d+\.\d+)` | Xray JSON (`v2ray-json`) only if `USE_CUSTOM_JSON_DEFAULT` or `USE_CUSTOM_JSON_FOR_HAPP` is set **and** the version is at least 1.63.1. Otherwise base64 links. |
| anything else | base64 links, `text/plain` |

The `USE_CUSTOM_JSON_*` flags default to False (`config.py:72-76`).

**Response headers** (subscription.py:69-79):
- `content-disposition: attachment; filename="<username>"`
- `profile-web-page-url`
- `support-url`
- `profile-title`: `base64:` plus b64 (`app/subscription/share.py:326`, `encode_title`)
- `profile-update-interval`: default `12`
- `subscription-userinfo: upload=0; download=<used>; total=<limit or 0>; expire=<unix or 0>`. Marzban reports all usage as download (subscription.py:40-45).

**HWID:** none in Marzban master. Its fork PasarGuard has HWID discussion (https://github.com/PasarGuard/panel/issues/260). That is UNVERIFIED; I did not read the code.

### Remnawave backend (2026-11-14)

**Paths.** The global prefix is `/api` (`libs/contract/api/routes.ts:3`) and the controller is `sub`.
- `GET /api/sub/:shortUuid`
- `GET /api/sub/:shortUuid/:clientType`, where clientType is one of `stash|singbox|mihomo|json|v2ray-json|clash` (`libs/contract/constants/subscription-template/template-type/request-template-type.constant.ts`)
- `GET /api/sub/:shortUuid/info`
- In production the public subscription domain sits behind `subscription-page`.

**Format selection** uses Subscription Response Rules (SRR). The default seed is in `prisma/seed/default/response-rules.ts`. Rules are checked in order:
- `accept` CONTAINS `text/html` → BROWSER
- user-agent REGEX `^(?:flclash|rabbit|flowvy|murge|mihomo|prizrak-box|koala-clash|clash(?:-verge|-nyanpasu|x meta|[-.]?meta))` → MIHOMO
- `^stash` → STASH
- `^sfa|sfi|sfm|sft|karing|singbox` → SINGBOX
- `^clash` → CLASH
- fallback → **XRAY_BASE64**

So by default **Happ gets base64 links**. It is upgraded to Xray JSON only when both of these hold:
- the setting `serveJsonAtBaseSubscription` is true (seed default `false`, `prisma/seed/seeders/8_seed-subscription-settings.ts:79`)
- the User-Agent is in `JSON_SUBSCRIPTION_FALLBACK_CLIENTS`: `^[Ss]treisand`, `^Happ\/`, `^INCY\/`, `^ktor-client`, `^V2Box`, `^io\.github\.saeeddev94\.xray\/`, `^v2rayNG\/x.y.z`, `^v2rayN\/x.y.z`, `^v2plus\/x.y.z`

Both are in `src/modules/subscription-template/constants/extended-clients.ts` and `subscription.service.ts:250-258`.

**"Extended" clients** receive `meta.serverDescription` in Xray JSON. They are `^Happ\/` and `^INCY\/` (plus the Mihomo forks), per `extended-clients.ts` and `generators/xray-json.generator.service.ts:271`.

**Response encryption.** An SRR can encrypt the response body with `age1` / `age1pq1` (`libs/contract/models/response-rules/response-rule-modifications.schema.ts:3-6`).

**Response headers.** `getUserProfileHeadersInfo` (`subscription.service.ts:615-650`) always sends:
- `content-disposition: attachment; filename=<username>`
- `subscription-userinfo` (`upload=0; download=<used>; total=<limit>; expire=<unix, or 0 if year 2099>`, from `src/modules/subscription/utils/get-user-info.headers.ts:14-22`)
- `subscription-refill-date` when traffic resets
- the admin's `customResponseHeaders`

The seed for `customResponseHeaders` is:
- `profile-title: rwEncodeBase64:Remnawave`
- `profile-update-interval: 12`
- `support-url`
- `profile-web-page-url: {{SUBSCRIPTION_URL}}`

`rwEncodeBase64:X` becomes `base64:` plus b64(UTF-8 X) (`src/common/utils/templates/template-parser.ts:16-26`).

**HWID device limit** (`src/common/utils/extract-hwid-headers/extract-hwid-headers.util.ts`):
- **Request headers:** `x-hwid` must match `/^[a-zA-Z0-9=-]{10,64}$/`. If it does not, the header is treated as missing. Optional headers are `x-device-os` (platform), `x-ver-os`, `x-device-model` and `user-agent`, all truncated.
- **Logic:** `subscription.service.ts:171-246` and `checkHwidDeviceLimit` at line 738.
  - It runs only if `hwidSettings.enabled` is true and the SRR does not set `disableHwidCheck`.
  - A user with `hwidDeviceLimit === 0` is unlimited (`limitBypassed`), though the device is still recorded.
  - If the header is missing, the result is `subscriptionAllowed=false` and `hwidNotSupported=true`.
  - The limit is `user.hwidDeviceLimit ?? hwidSettings.fallbackDeviceLimit`.
- **What the client gets when blocked:** in this commit the code returns **HTTP 200**, not 404 (`subscription.controller.ts:98-102`, where only `SubscriptionNotFoundResponse` gives a 404).
  - Body: empty `text/plain`, or "custom remarks" placeholder configs when `isShowCustomRemarks` is on.
  - Headers: `x-hwid-active: true` (unless bypassed), `x-hwid-not-supported: true` and/or `x-hwid-max-devices-reached: true`, `x-hwid-limit: true` (commented "v2rayTUN"), and an optional `announce` built from `hwidSettings.maxDevicesAnnounce`.
  - Internal errors return 404.
- **Docs disagree with the code.** The docs say "Remnawave will return a 404 error if no HWID header is sent" (https://docs.rw/docs/features/hwid-device-limit). The code at the pinned commit returns 200 with an empty or placeholder body. **Zatvor must handle both.**
- The docs list Happ, v2RayTun, Koala Clash, FlClashX, Incy and others as HWID-capable clients. They mention the `HWID_FALLBACK_DEVICE_LIMIT` env variable (from the same docs page).

### Remnawave subscription-page (2026-10-22)
- It proxies to the backend and drops requests without a User-Agent (`backend/src/common/middlewares/header-filter.middleware.ts`, which destroys the socket). **Zatvor must always send a User-Agent.**
- It forwards response headers except `IGNORED_HEADERS` (`backend/src/common/axios/axios.service.ts:315`).
- It builds Happ crypto links with the npm package `@kastov/cryptohapp` (`HAPP_CRYPT3_LINK` / `HAPP_CRYPT4_LINK`) and INCY ones with `@incy/link-encoder` (`frontend/src/shared/utils/template-engine.ts:1-27`).
- The default app config uses `happ://add/{{SUBSCRIPTION_LINK}}` (`remnawave_backend/src/modules/subscription-page-configs/constants/default-subpage-config.ts:292`).

### Marzneshin (2025-10-03)
- **Path:** `/{username}/{key}`, plus `/info`, `/usage`, and `/{client_type}` where `client_type` matches `^(sing-box|clash-meta|clash|xray|v2ray|links|wireguard)$` (`app/routes/subscription.py`).
- **Format rules** are user-editable regex rules stored in the DB. The default migration is `app/db/migrations/versions/20240608_9001356f0063_create_settings.py:41-60`.
  - Clash-verge/meta → clash-meta; clash/stash → clash; sing-box clients → sing-box.
  - Newer v2rayN/v2rayNG → xray JSON. Streisand → xray.
  - The catch-all `.*` → `base64-links`.
  - So **Happ gets base64 links**.
  - A rule result of `block` returns 404.
- **Headers** are the same set as Marzban, including `base64:` titles.

---

## 3. Happ documentation (happ.su)

Pages used:
- https://www.happ.su/main/dev-docs/app-management.md
- https://www.happ.su/main/dev-docs/routing.md and the `ru` version
- https://www.happ.su/main/dev-docs/crypto-link.md
- https://www.happ.su/main/dev-docs/encrypting-subscription-content.md
- https://www.happ.su/main/dev-docs/hwid-links.md
- https://www.happ.su/main/dev-docs/examples-of-links-and-parameters.md

### Subscription headers
Source: app-management.md.

**Delivery rules:**
- Each parameter can be sent as an HTTP response header **or** as a `#name: value` line in the subscription body.
- Booleans: `true` or `1` turns a setting on. Any other non-empty value turns it off.
- "Advanced" parameters only take effect when a Provider ID is set (provider-id.md).

**Basic headers:**

| Header | Meaning |
|---|---|
| `profile-title` | Max 25 chars. Plain text or base64. In practice panels send a `base64:` prefix; see section 5. |
| `subscription-userinfo` | `upload=; download=; total=; expire=` |
| `profile-update-interval` | Hours |
| `support-url` | Support link. A Telegram link shows a Telegram icon. |
| `profile-web-page-url` | Website link |
| `announce` | Max 200 chars. Plain text or `base64:` |
| `routing` | Value is a `happ://routing/...` deep link |
| `routing-enable` | Turns routing on or off |
| `hide-settings` | Hides the server configuration from the user |

**Happ-specific headers, by group:**
- **Info block:** `sub-info-text`, `sub-info-color` (red, blue or green), `sub-info-button-text`, `sub-info-button-link`. Send `0` to turn the block off.
- **Expiry:** `sub-expire`, `sub-expire-button-link`, `notification-subs-expire`.
- **Subscription source:** `new-url`, `new-domain`, `fallback-url` (used on HTTP 300-599 or a 9 s timeout; it can also go in the URL fragment as `#Name?fallback-url=`).
- **Updates and requests:** `subscription-auto-update-enable`, `subscription-auto-update-open-enable`, `subscription-request-timeout`, `change-user-agent`, `manual-block-user-agent`, `user-agent-geo-files`.
- **Autoconnect:** `subscription-autoconnect`, `subscription-autoconnect-type` (`lastused`, `lowestdelay` or `random`).
- **Ping:** `subscription-ping-onopen-enabled`, `ping-type` (`proxy`, `proxy-head`, `tcp` or `icmp`), `check-url-via-proxy`, `proxy-ping-mode`, `proxy-ping-timeout`, `ping-result`.
- **List display:** `subscriptions-sort-type`, `subscriptions-collapse`, `subscriptions-expand-now`, `subscription-pin`.
- **Network behavior:** `dont-use-filter`, `autoswitch-on-wifi`, `autoswitch-on-wifi-timeout`, `app-auto-start`, `hide-vpn-icon`.
- **HWID:** `subscription-always-hwid-enable`, `subscription-alternative-hwid-enabled`.
- **TUN / proxy mode:** `proxy-enable`, `tun-enable`, `tun-mode` (`system` or `gvisor`), `tun-type` (`singbox`, `tun2proxy`, `default` or `xray`), `custom-tunnel-config`.
- **Tunnel routes:** `exclude-routes`, `exclude-routes-set`, `xray-tun-enable`, `xray-tun-mtu`, `block-bind-to-tunnel-enable`, `include-all-networks-enable`, `exclude-local-networks-enable`, `exclude-apns-enable`.
- **Inbounds and DNS:** `inbound-http-enable`, `dns-from-json-enable`.
- **Local proxy auth:** `socks-auth-mode` and `http-auth-mode` (`auto`, `manual`, `from-json` or `disable`), plus `-user` and `-password` variants.
- **Sniffing and mux:** `sniffing-enable`, `mux-enable`, `mux-tcp-connections`, `mux-xudp-connections`, `mux-quic`.
- **No-limit modes:** `no-limit-enabled`, `no-limit-xhttp-enabled`.
- **Fragmentation:** `fragmentation-enable` and `fragmentation-*` parameters.
- **Noises:** `noises-enable` and `noises-*` parameters.
- **Server address resolve:** `server-address-resolve-enable`, `server-address-resolve-dns-domain`, `server-address-resolve-dns-ip`.
- **Per-app proxy:** `per-app-proxy-mode` (`off`, `on` or `bypass`), `per-app-proxy-list`, `per-app-proxy-list-invert`, `per-app-proxy-list-set`, and the `-windows`, `-macos` and `-linux` variants with `-add` and `-set`.
- **Theme:** `color-profile`.

**Server description.** `serverDescription` (max 30 chars) is carried in the link fragment as `#Title?serverDescription=<base64>`, or in JSON as `"meta": {"serverDescription": ...}` (examples page).

**Body encryption.** Body encryption is documented as AES-128-GCM with a fixed IV `kkkkkkkkkkkk`.
- The key ID goes in the URL as `?key=keyNN`.
- The tag goes in the `encrypt-tag` header.
- The body is base64 ciphertext.
- Keys are pre-shared and embedded in the app (encrypting-subscription-content.md).

**Request headers Happ sends.** These are NOT documented on happ.su. The `x-hwid`, `x-device-os`, `x-ver-os` and `x-device-model` set comes from the Remnawave docs and from both panels' code (section 2). The exact Happ User-Agent string is UNVERIFIED. Panels match it as `^Happ/` (Remnawave), `^Happ/(\d+\.\d+\.\d+)` (Marzban) and `\bhapp\b` (3x-ui), which implies `Happ/<x.y.z>...`.

### Routing profile format (fixture keys)
Sources: routing.md (EN and RU, via WebFetch summaries). All values are strings, including booleans and timestamps.

| Key | Meaning |
|---|---|
| `Name` | Profile name. A profile with the same name is overwritten on import. |
| `GlobalProxy` | `"true"` sends unmatched traffic through the proxy. `"false"` sends it direct. Default true. |
| `UseChunkFiles` | Splits geo files into per-tag chunks to save memory, especially on iOS. The tags must match the JSON subscription's tags. |
| `RemoteDns`, `DomesticDns` | Present in the fixture. Not described in the docs I fetched. Looks like a legacy/plain DNS IP. UNVERIFIED. |
| `RemoteDNSType`, `RemoteDNSDomain`, `RemoteDNSIP` | DNS for proxied traffic. Type is `DoH` or `DoU`; the docs say the default is DoU. The domain is required for DoH (default `https://cloudflare-dns.com/dns-query`). Default IP `1.1.1.1`. |
| `DomesticDNSType`, `DomesticDNSDomain`, `DomesticDNSIP` | DNS for direct traffic. Same types. Default IP `8.8.8.8`. |
| `Geoipurl`, `Geositeurl` | URLs of the geoip/geosite `.dat` files. The settings text spells them `geoipUrl`/`geositeUrl`. Downloads time out after 3 minutes. Updates happen at most weekly. The old files stay active until the new ones finish downloading. |
| `LastUpdated` | Unix timestamp string. Geo files are re-downloaded when it is newer than the stored value, or when no value is stored. |
| `DnsHosts` | Map of domain to IP, mapped to Xray `dns.hosts`. |
| `DirectSites` / `ProxySites` / `BlockSites` | Xray domain rules: `geosite:`, `domain:`, `full:`, `regexp:` and similar. |
| `DirectIp` / `ProxyIp` / `BlockIp` | Xray IP rules: `geoip:` or CIDR. |
| `RouteOrder` | Order in which rule groups are evaluated; the first match wins. The docs list no values. The fixture's `block-proxy-direct` is presumably a dash-separated permutation of block, proxy and direct (UNVERIFIED). |
| `DomainStrategy` | `AsIs`, `IPIfNonMatch` or `IPOnDemand`. Same meaning as Xray `routing.domainStrategy`. |
| `FakeDNS` | `"true"` or `"false"`. Turns on Xray FakeDNS. |

The fixture uses non-standard tags `geosite:whitelist`, `geoip:direct`, `geosite:win-spy` and `geosite:escapefromtarkov`. These exist only in the custom roscomvpn dat files at the fixture's `Geoipurl`/`Geositeurl` (UNVERIFIED that every tag exists). **Zatvor must download the dat files the profile names, not the bundled Loyalsoldier ones.**

### Deep links
Source: routing.md.

| Link | Effect |
|---|---|
| `happ://routing/add/<base64 JSON>` | Adds the profile. It becomes active after its geo files download. |
| `happ://routing/onadd/<base64 JSON>` | Adds the profile and activates it immediately. |
| `happ://routing/off` | Turns routing off. |
| `happ://add/<subscription URL>` | Adds a subscription (Remnawave `default-subpage-config.ts:292`). |
| `happ://crypt4/...`, `happ://crypt5/...` | Encrypted subscription URLs (crypto-link.md). Older `crypt3` links are produced by `@kastov/cryptohapp` v1.1.2, which supports v2, v3 and v4 (npm README). |

The routing link can arrive by clipboard, QR code, the `routing` header, or a line in the subscription body.

### happ://crypt*: proprietary
- crypto-link.md describes encryption only: via crypto.happ.su, an API at `https://crypto.happ.su/api-v2.php`, or an RSA-4096 public key that it marks as deprecated.
- It says the keys are embedded in the app and that the purpose is to hide the subscription URL so the user "cannot edit, view, or share" it.
- No decryption procedure is published. The page also has text asking AI assistants not to help decrypt protected links.
- The only public descriptions of the algorithm come from reverse engineering: the 3x-ui `happ_crypto.go` comment cites `Omegaplexx/hpwnr`.

**Conclusion: proprietary and closed by design.** A GPL client cannot decrypt these links without Happ's private key. **Recommendation:** do not support `happ://crypt*`. Show a clear "unsupported encrypted link" error. The same applies to INCY crypt links and to Remnawave `age` body encryption unless the user supplies the key.

### Fixture decode and diff

Command:
```
P=$(tr -d '\n\r ' < happ-routing-link.txt); P=${P#happ://routing/onadd/}
python3 -I -c "import base64,json,sys; p=sys.argv[1]; raw=base64.b64decode(p+'='*(-len(p)%4)); b=json.load(open('happ-routing.json')); print(len(raw), json.loads(raw)==b, json.dumps(b,separators=(',',':'),ensure_ascii=False).encode()==raw)" "$P"
```

Output: `1254 True True`.

- The payload is 1672 characters of standard base64. `len % 4 == 0`, so it has no padding issue, and it contains no `-` or `_`.
- It decodes to 1254 bytes of compact JSON.
- That JSON is **semantically equal** to `happ-routing.json`, with the **same key order**.
- It is **byte-identical** to `json.dumps(fixture, separators=(',',':'))`.
- **Diff: none.**
- One trap: piping the payload through `echo` adds a newline and makes `base64 -d` fail with "invalid input". The decoder should strip whitespace and accept a missing pad, as 3x-ui does.

---

## 4. Share-link formats

### Canonical specs
- **VLESS / VMessAEAD / Trojan-style URI:** XTLS "VMessAEAD / VLESS Share Link Standard Proposal", started 2020-12-19 by DuckSoft (https://github.com/XTLS/Xray-core/discussions/716).
  - Query parameters: `type` (`tcp` = RAW, `kcp`, `ws`, `http`, `grpc`, `httpupgrade`, `xhttp`), `security` (`none`, `tls` or `reality`), `encryption` (VLESS: `none` or `mlkem768x25519...`), `flow` (e.g. `xtls-rprx-vision`), `sni`, `fp`, `alpn` (comma-separated), `ech`, `pcs`, `vcn`.
  - REALITY: `pbk` (required), `sid`, `pqv`, `spx`.
  - ws, httpupgrade and xhttp: `path` (default `/`) and `host`. xhttp also takes `mode` and `extra` (URL-encoded JSON).
  - grpc: `serviceName`, `mode` (`gun`, `multi` or `guna`), `authority`. mKCP: `mtu`, `tti`. Also `fm` (finalmask JSON).
  - `headerType` and `seed` appear only in examples.
  - Encoding: every value is encodeURIComponent-encoded, and the `#remark` too. IPv6 hosts go in `[...]`. IDNs use punycode. No parameter may repeat.
- **VMess legacy:** `vmess://` plus base64 of a JSON object with `v=2, ps, add, port, id, aid, scy, net, type, host, path, tls, sni, alpn, fp`.
  - Reference: v2rayN `ServiceLib/Handler/Fmt/VmessFmt.cs`, where `ResolveStdVmess` tries the URI form first and falls back to `ResolveVmess`, the base64 JSON form.
  - v2rayN exports `net=tcp` for raw (`Global.RawNetworkAlias = "tcp"`, `ServiceLib/Global.cs:55`).
- **Shadowsocks SIP002:** `ss://userinfo@host:port[/][?plugin=...][#tag]` (https://shadowsocks.org/doc/sip002.html).
  - `userinfo` is base64url(`method:password`), or percent-encoded plain text.
  - AEAD-2022 (SIP022) userinfo MUST NOT be base64-encoded.
  - `plugin=` holds `name;opts`.
  - **Legacy** form: `ss://base64(method:password@host:port)#tag`. v2rayN's `ShadowsocksFmt.cs:10` tries `ResolveSSLegacy` and then `ResolveSip002`.
- **Hysteria2:** `hysteria2://` or `hy2://[auth@]host[:port]/?obfs=salamander|gecko&obfs-password=&sni=&insecure=0|1&pinSHA256=#name` (https://v2.hysteria.network/docs/developers/URI-Scheme/).
  - Multi-port is written like `:123,5000-6000`. The default port is 443. `auth` is percent-encoded, and `user:pass` is used for userpass auth.
  - v2rayN (`Hysteria2Fmt.cs:103-254`) also reads `mport`, `minPacketSize`/`maxPacketSize` (gecko) and `ech`. When `pinSHA256` is present it forces insecure with the pin (comment at lines 179-183).
  - Happ additionally documents `port` multi-list and `mportHopInt` (examples page).
- **SOCKS** (Happ examples page) accepts three forms: `socks://user:pass@host:port`, `socks://<b64(user:pass)>@host:port#Name`, and `socks://<b64 of whole string>`. v2rayN reference: `SocksFmt.cs`.
- **Trojan:** `trojan://password@host:port?security=tls&sni=&type=...#name`, using the same transport and security parameters as above. v2rayN reference: `TrojanFmt.cs`.

### Reference parser
- v2rayN `v2rayN/ServiceLib/Handler/Fmt/` at commit `9bb74a0`.
- Shared transport and security query handling is in `BaseFmt.cs:20-290`. It reads `security, sni, alpn, fp, pbk, sid, spx, pqv, ech, vcn, pcs, fm, type, headerType, host, path, seed, mtu, mode, extra, authority, serviceName`.
- Protocol files: `VLESSFmt.cs` (`encryption` default `none`, `flow`), `VmessFmt.cs`, `TrojanFmt.cs`, `ShadowsocksFmt.cs`, `Hysteria2Fmt.cs`, `SocksFmt.cs`.
- Tests are in `ServiceLib.Tests/Fmt/`.

### Edge cases to handle
- `type=raw` vs `tcp`: export `tcp`.
- For xhttp, `extra` is URL-encoded JSON that may contain `downloadSettings`.
- For REALITY with no `fp`, default to `chrome`.
- `flow=xtls-rprx-vision` is valid only with tcp/raw plus tls/reality.
- gRPC `mode=multi`.
- `alpn` arrives as `h2%2Chttp%2F1.1`.
- Remarks may be percent-encoded UTF-8 with emoji flags.
- Happ adds `?serverDescription=<b64>` inside the fragment.
- Fragments and noises: `fragment=len,int,packets`, `noises=type,packet,delay` (Happ examples).
- Subscription bodies may be:
  - base64 Std or URL-safe, with or without padding and with line wraps, or plain text
  - lines starting `#` that carry `#name: value` header equivalents
  - a `happ://routing/...` line
  - a JSON array of full Xray configs, each with optional `remarks` and `meta`

---

## 5. `subscription-userinfo` and `profile-title`

- **Format:** `upload=<bytes>; download=<bytes>; total=<bytes>; expire=<unix seconds>`. The separator is `; `.
  - 3x-ui writes it with exactly this format string (`calendar_expire.go:20`).
  - Clash Verge Rev parses it by splitting on `;`, trimming, and splitting on `=`. Missing or invalid fields become 0 (`cvr/src-tauri/src/utils/help.rs:215-223`).
  - Clash Verge Rev accepts the header name case-insensitively, and also with any `<prefix>-` such as `x-amz-meta-subscription-userinfo` (`cvr/src-tauri/src/config/prfitem.rs:296-312`).
- **Conventions:** `total=0` means unlimited, and `expire=0` means it never expires. Marzban and Remnawave emit 0 for "no limit" (Remnawave also when the expiry year is 2099), and both always send `upload=0` with all usage in `download`. Used traffic = upload + download.
- **Clash naming:** Clash Verge takes the profile name from `Content-Disposition` (`filename*` RFC 5987 first, then `filename`), not from `profile-title` (`prfitem.rs:317-338`).
  - `profile-update-interval` is in hours; Clash Verge multiplies it by 60 to get minutes (prfitem.rs:342-347).
  - Clash Verge also reads `profile-web-page-url`.
- **v2rayN** does not parse `subscription-userinfo` or `profile-title`. `grep -rn -i 'subscription-userinfo\|profile-title' --include=*.cs` returned nothing.
- **`profile-title` encoding:** the de-facto rule is `base64:` followed by standard base64 (with padding) of the UTF-8 bytes. This is what Marzban (`share.py:326`), Marzneshin, Remnawave (`template-parser.ts:16-26`) and 3x-ui (`controller.go:963`) all send.
  - The same `base64:` prefix is used for `announce` (3x-ui, Remnawave, and the Happ docs).
  - Happ's docs say profile-title may be "plain text or base64" with a 25-char limit.
- **Parser recommendation:**
  1. If the value starts with `base64:`, decode it, tolerating URL-safe or missing padding.
  2. Otherwise, if the raw header bytes are valid UTF-8, use them as they are. Some servers send raw UTF-8, or RFC 2047 or percent-encoding (UNVERIFIED).
  3. Cap the length for display.
