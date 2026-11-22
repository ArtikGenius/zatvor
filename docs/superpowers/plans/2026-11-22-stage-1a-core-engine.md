# Stage 1a: Core Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A pure-Go engine that turns a subscription (links, Xray JSON, Happ routing) into a validated Xray config with our inbounds and rule layers, and runs the pinned Xray with it, proven in CI by real connections over VLESS Reality+Vision, VLESS XHTTP and Hysteria2+finalmask.

**Architecture:** Stage 1 is split into three plans. **1a (this plan)** is the OS-independent core: parsing, subscriptions, Happ translation, config generation, geo merge, validation, encrypted store and the Xray runner. **1b** is zatvord on Windows: state, local API, journal, system proxy, supervisor, scheduled updates, dev-tool routing, diagnostics, CLI `status`/`check`. **1c** is the Tauri tray and panel, first run, ru/en, MSI and Artur's acceptance. Every 1a package is a library with no OS side effects; time, network and filesystem come in as parameters.

**Tech Stack:** Go 1.27.4; Xray-core v26.11.9 as a binary (runtime) and as a Go module (`infra/conf` types for schema checks, `common/geodata` protobuf for geo files); `golang.org/x/sync/singleflight`.

**Spec:** `docs/superpowers/specs/2026-11-19-zatvor-design.md` (sections 3, 5, 6, 11-13, 16). Pitfalls: `docs/known-pitfalls.md` §2.5-2.7. Research: `docs/research/subscriptions.md`, `docs/research/xray-core.md`.

## Global Constraints

- Go `go 1.27` + `toolchain go1.27.4`; run with `GOTOOLCHAIN=go1.27.4`. Module `github.com/ArtikGenius/zatvor`.
- Xray pinned to v26.11.9 (`third_party/xray/pin.json`); any bump is a separate reviewed change.
- `go test ./...` has no side effects and no network: temp dirs, random free ports, fake clocks. Tests that run the real Xray binary use the `xrayint` build tag (existing pattern in `internal/xrayint`).
- Configs are produced by a serializer, never by string concatenation (pitfall 55).
- Provider `routing`/`dns` keep their content and relative order; we only **prepend** our rules (spec B3). Provider key order is preserved (reuse the ordered JSON codec).
- One user action = exactly one subscription request; no parallel UA variants (spec §5.6).
- UA default `Zatvor/<version> (<os>)`; always sent (spec §5.3).
- HWID = first 32 hex chars of `HMAC-SHA256(key="zatvor-hwid-v1", machine_id ‖ subscription_host)`; frozen forever (ADR 0008).
- Secrets never appear in logs, errors, test names or golden files; fixtures use `example.com` hosts and `REPLACE_ME` secrets. The dev subscription link is never committed.
- No telemetry; no request to any host except the subscription URL the user gave, the geo URLs a profile names, and IP-check services (1b).
- Errors shown to users are plain language; Russian/English strings live in 1c, 1a returns typed errors with codes.

## Review Focus

1. A subscription body with BOM, CRLF, URL-safe unpadded base64, or one broken line among good ones: import the good nodes, warn per bad node, never fail the whole subscription (pitfalls 43, 44). Owner: Task 2.
2. An update that returns an empty body, an HTML error page, or a 200 with a Remnawave `x-hwid-max-devices-reached` placeholder: keep the last working copy, report the human reason (pitfall 47, spec §3). Owner: Task 5.
3. Remarks and passwords containing quotes, backslashes, `#`, `%`, emoji and Cyrillic survive parse → generate → Xray `-test` (pitfall 55). Owner: Tasks 1 and 7.
4. A provider routing rule that references `inboundTag` of the provider's own inbound still matches after we replace the inbounds (spec R3.3). Owner: Task 7.
5. A profile needing a geo tag that neither bundled database has: a human error naming the tag and database before Xray starts, not an Xray crash (spec §6.5). Owner: Task 8.

---

## File Structure

```
internal/ojson/            ordered JSON value + codec (moved from gen/noisemig)
internal/link/             share links <-> Node; Node -> Xray outbound
internal/subbody/          body detection and decoding
internal/subhdr/           response header parsing
internal/hwid/             HWID and device headers
internal/subfetch/         the one-request fetcher
internal/happ/             Happ routing profile -> Xray routing + dns
internal/profile/          Profile, Subscription, stable IDs, update merge
internal/store/            encrypted profile store
internal/gen/              config builder: inbounds, api, log, rule layers, presets
internal/geo/merge/        merged geoip.dat / geosite.dat per tag set
internal/validate/         schema check + xray -test
internal/xrayrun/          Xray process runner and latency test
internal/e2e/              (xrayint) full pipeline against local Xray servers
testdata/links/            link corpus (one file per scheme) with expected nodes
testdata/subs/             subscription bodies and header sets
testdata/golden/gen/       generated configs
```

---

### Task 1: Ordered JSON package and share-link parser

**Files:**
- Create: `internal/ojson/ojson.go`, `internal/ojson/ojson_test.go`
- Modify: `internal/gen/noisemig/*.go` (use `ojson`, delete `orderedjson.go`)
- Create: `internal/link/node.go`, `internal/link/parse.go`, `internal/link/export.go`, `internal/link/outbound.go`, `internal/link/*_test.go`, `testdata/links/*.txt`

**Interfaces:**
- Produces:
  - `ojson.Parse([]byte) (any, error)`, `ojson.Marshal(any) ([]byte, error)`, `type ojson.Object` with `Get(key) (any, bool)`, `Set(key, any)`, `Delete(key)`, `Keys() []string`. Same semantics as the noisemig codec (numbers as `json.Number`, key order kept).
  - `type link.Node struct { Scheme, Remark, Address string; Port int; Params map[string]string; Raw string }`, where `Params` carries every query/userinfo field under its share-link name (`id`, `flow`, `security`, `sni`, `fp`, `pbk`, `sid`, `spx`, `type`, `path`, `host`, `mode`, `extra`, `serviceName`, `alpn`, `insecure`, `obfs`, `obfs-password`, `pinSHA256`, `method`, `password`, `user`, `aid`, ...).
  - `link.Parse(line string) (Node, error)`; `link.Export(Node) string`; `link.Outbound(n Node, tag string) (*ojson.Object, error)`; `link.ID(Node) string` = hex SHA-256 of scheme, address, port, credential (`id`/`password`/`user`) and transport (`type`,`security`,`path`,`host`,`serviceName`), first 16 chars (pitfall 46).
  - Errors: `link.ErrUnsupported` (unknown scheme), `link.ErrEncrypted` (`happ://crypt`, `happ://crypt2..5`, INCY), wrapped with the scheme only, never the line.

- [ ] **Step 1: Move the codec.** Create `internal/ojson` from `noisemig/orderedjson.go` with exported names; switch noisemig to it. Run `go test ./internal/gen/noisemig/...`: golden tests still PASS unchanged.
- [ ] **Step 2: Write the corpus tests.** `testdata/links/<scheme>.txt` holds lines `<link>\t<expected Node as JSON>`. Cover: vless reality+vision (`flow=xtls-rprx-vision`, `pbk`, `sid`, `spx`, `fp`), vless xhttp with `extra` (URL-encoded JSON), vless ws+tls, vless grpc (`serviceName`, `mode=multi`), vless `security=none`; vmess v2rayN base64 JSON (`aid`, `net`, `type`, `host`, `path`, `tls`); trojan with `sni` and `type=ws`; ss SIP002 (`ss://base64(method:pass)@host:port#name`), SIP002 with plugin, legacy (`ss://base64(method:pass@host:port)#name`), 2022 method with `:` in password; hysteria2 and hy2 with `obfs=salamander`, `obfs-password`, `sni`, `insecure=1`, `pinSHA256`, multi-port `host:443,8443` (kept as given in `Params["ports"]`); socks in three forms (`socks://user:pass@h:p`, `socks://base64(user:pass)@h:p`, `socks://base64(user:pass@h:p)`); remarks with `#`, `%25`, emoji, Cyrillic, quotes, backslash. `TestParseCorpus` asserts `Parse(link)` deep-equals the expected Node (ignoring `Raw`). `TestEncrypted`: `happ://crypt3/...` returns `ErrEncrypted`. `TestUnknownScheme`: `tuic://...` returns `ErrUnsupported`.
- [ ] **Step 3: Run** `go test ./internal/link/...` → FAIL (package missing).
- [ ] **Step 4: Implement `Parse` and `Export`.** Study v2rayN `ServiceLib/Handler/Fmt/*` at `9bb74a0` for edge cases (do not copy). IPv6 hosts in brackets; port range check 1-65535; remarks are URL-decoded once.
- [ ] **Step 5: Round-trip test.** `TestRoundTrip`: for every corpus line, `Parse(Export(Parse(l)))` equals `Parse(l)` (pitfall 45). `TestRemarkSerializer`: a Node with remark `a"b\c#d%e ✓ Ж` exports and re-parses unchanged.
- [ ] **Step 6: Implement `Outbound`.** Map to Xray v26.11.9 outbound JSON: vless/vmess/trojan/shadowsocks/socks under `settings`; transports under `streamSettings` (`realitySettings`, `tlsSettings`, `xhttpSettings` with `extra` merged as an object, `wsSettings`, `grpcSettings`); hysteria2 as `protocol:"hysteria"` with `settings.version:2`, `streamSettings.network:"hysteria"`, `hysteriaSettings.auth`, and salamander as `finalmask.udp:[{type:"salamander",settings:{password}}]` (same shape as `testdata/fixtures/xray-hy2-dns-finalmask.json`). Test `TestOutboundHy2MatchesFixture`: the hysteria2 link equivalent of the fixture's proxy outbound produces a JSON-equal outbound (minus the noise item, which links cannot carry).
- [ ] **Step 7: Run** `go test ./internal/link/... ./internal/ojson/... ./internal/gen/...` → PASS. **Commit** `feat(link): share-link parser, exporter and outbound mapping`.

### Task 2: Subscription body detection and decoding

**Files:**
- Create: `internal/subbody/body.go`, `internal/subbody/body_test.go`, `testdata/subs/*`

**Interfaces:**
- Consumes: `link.Parse`, `link.ErrEncrypted`, `ojson.Parse`.
- Produces:
  - `type subbody.Kind int` with `XrayJSON`, `Links`, `Clash`, `Encrypted`, `Empty`, `Unknown`.
  - `type subbody.Result struct { Kind Kind; Configs []*ojson.Object; Nodes []link.Node; Skipped []Skip; InlineHeaders map[string]string }`, `type Skip struct { Line int; Scheme string; Reason string }`.
  - `subbody.Decode(body []byte) Result`.

- [ ] **Step 1: Failing tests** in `body_test.go` over files in `testdata/subs/`:
  - `TestJSONObject` (the finalmask fixture as a single object) → `XrayJSON`, 1 config; `TestJSONArray` (array of 2) → 2 configs; an object without `outbounds` → `Unknown`.
  - `TestBase64Variants`: the same 3-link body as std base64, URL-safe, unpadded, with CRLF line wraps and a UTF-8 BOM → `Links` with the same 3 nodes each (pitfall 43).
  - `TestPlainLines`: plain links with blank lines and `#profile-title: base64:...` / `#subscription-userinfo: upload=0; download=0; total=0; expire=0` → `InlineHeaders` keys lower-cased.
  - `TestOneBadLine`: 2 good links and one `vless://broken` → 2 nodes, `Skipped` has `{Line:2, Scheme:"vless"}` (pitfall 44).
  - `TestClash`: a `proxies:` YAML → `Clash`. `TestEncryptedBody`: one `happ://crypt4/...` line → `Encrypted`. `TestEmpty`: whitespace → `Empty`. `TestHTML`: `<!doctype html>` → `Unknown`.
  - `TestTenThousandNodes`: a generated 10k-line body decodes in under 1 s (pitfall 50).
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement `Decode`.** Order: strip BOM and whitespace; JSON if first byte is `{`/`[`; else try base64 (std, URL, raw std, raw URL) on whitespace-stripped body and accept only if the result has at least one line with a known scheme; else plain lines; YAML Clash detected by a `proxies:` line. Detection is by body only (spec §5.2).
- [ ] **Step 4: Run** → PASS. **Commit** `feat(subbody): tolerant subscription body decoder`.

### Task 3: Response headers and HWID

**Files:**
- Create: `internal/subhdr/headers.go`, `internal/subhdr/headers_test.go`, `internal/hwid/hwid.go`, `internal/hwid/hwid_test.go`

**Interfaces:**
- Produces:
  - `type subhdr.Info struct { Upload, Download, Total int64; Expire time.Time; Title string; UpdateInterval time.Duration; SupportURL, WebPageURL, Announce string; RoutingLink string; RoutingEnabled bool; HWIDNotSupported, HWIDLimitReached bool; HappLike bool }`. `Total==0` means unlimited; zero `Expire` means none.
  - `subhdr.Parse(h http.Header, inline map[string]string, contentDisposition string) Info` (inline body headers fill gaps; real headers win).
  - `hwid.ID(machineID, subscriptionHost string) string`; `hwid.Headers(machineID, host string, dev Device, opts Options) http.Header` with `type Device struct{ OS, OSVersion, Model string }`, `type Options struct{ SendDevice bool }` (x-hwid always; `x-device-os`, `x-ver-os`, `x-device-model` only when `SendDevice`, default true per spec §5.5). Note: pitfall 51 asks for opt-in device identifiers; the approved spec §5.5 sends them by default with a visible switch and a README section. Follow the spec; the UI (1c) shows the switch on the subscription.
  - `hwid.MachineID() (string, error)` declared in `machineid_{windows,darwin,linux}.go` (Windows `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` read-only; macOS `IOPlatformUUID` via `ioreg -rd1 -c IOPlatformExpertDevice`; Linux `/etc/machine-id`).

- [ ] **Step 1: Failing tests.**
  - `TestUserinfo`: `upload=1; download=2; total=0; expire=0` → Total 0 (unlimited), zero Expire; tolerant of spaces, missing keys and `expire=1795000000` (pitfall 48).
  - `TestTitle`: `base64:0JfQsNGC0LLQvtGA` → `Затвор`; plain title kept; falls back to the `content-disposition` filename.
  - `TestRouting`: `routing: happ://routing/onadd/<b64>` with no `routing-enable` → RoutingEnabled true; `routing-enable: false` → false.
  - `TestHWIDResponses`: `x-hwid-not-supported: true` and `x-hwid-max-devices-reached: true` set the flags; 3x-ui style `X-Hwid-Limit` too.
  - `TestHappLike`: true when any of `routing`, `announce`, `hide-settings`, `sub-info-*` present (spec §5.3 hint).
  - `TestHWIDVector` (golden, freezes the algorithm): `hwid.ID("00000000-0000-0000-0000-000000000000", "sub.example.com")` equals the value computed once and committed; matches `^[a-zA-Z0-9=-]{10,64}$`; differs for another host.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS.
- [ ] **Step 5: ADR.** Add the golden vector to `docs/decisions/0008-hwid-algorithm.md`. **Commit** `feat(subhdr,hwid): header parsing and frozen HWID`.

### Task 4: Happ routing profile translation

**Files:**
- Create: `internal/happ/routing.go`, `internal/happ/routing_test.go`

**Interfaces:**
- Consumes: `ojson`.
- Produces: `type happ.Profile` (fields of `testdata/fixtures/happ-routing.json`, exact JSON names); `happ.DecodeLink(string) (Profile, error)` for `happ://routing/onadd/<b64>` and `.../add/...`; `happ.DecodeJSON([]byte) (Profile, error)`; `happ.Translate(p Profile, proxyTag, directTag, blockTag string) (routing, dns *ojson.Object, err error)`; `happ.GeoURLs(p Profile) (geoip, geosite string)`.

- [ ] **Step 1: Golden test.** `TestTranslateMatchesLegacyFixture`: `Translate(DecodeLink(happ-routing-link.txt), "proxy","direct","block")` produces `routing` and `dns` JSON-equal to those of `testdata/fixtures/xray-hy2-dns-legacy.json` (spec Q2.6: this is the golden test). `TestDecodeLinkEqualsJSON`: link and JSON fixtures decode to the same Profile. `TestRouteOrder`: `RouteOrder` `block-proxy-direct` vs `proxy-direct-block` changes only the order of the three rule groups. `TestGlobalProxyFalse`: final rule goes to direct.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** using 3x-ui `internal/sub/json_routing.go` at `v3.10.0` and research §3 "Routing profile format" as references (do not copy). **Step 4: Run** → PASS. **Commit** `feat(happ): routing profile translation`.

### Task 5: The one-request fetcher

**Files:**
- Create: `internal/subfetch/fetch.go`, `internal/subfetch/fetch_test.go`

**Interfaces:**
- Consumes: `subbody.Decode`, `subhdr.Parse`, `hwid.Headers`.
- Produces:
  - `type subfetch.Request struct { URL string; UA string; HWID http.Header; Via Dialer }`, `type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)` (nil = direct; 1b passes the tunnel's authenticated SOCKS dialer, spec R3.6).
  - `type subfetch.Result struct { Body subbody.Result; Info subhdr.Info; Status int; Problem Problem }`, `type Problem int` with `None`, `Network`, `HTTPStatus`, `EmptyBody`, `NotASubscription`, `DeviceLimit`, `HWIDRequired`, `Encrypted`, `ClashUnsupported`.
  - `subfetch.New(timeout time.Duration) *Fetcher`; `(*Fetcher).Fetch(ctx, Request) (Result, error)`. Concurrent calls for the same URL share one request (singleflight). No retries inside `Fetch` (retries are the scheduler's, 1b).
  - `subfetch.DefaultUA(version, os string) string` = `Zatvor/<version> (<os>)`; `subfetch.HappCompatibleUA(version, os)` = `Zatvor/<version> (<os>; happ-compatible)`; `subfetch.HappUA(pinned, os)` = `Happ/<pinned>/<Windows|macos>/zatvor`.

- [ ] **Step 1: Failing tests** with `httptest.Server`:
  - `TestOneRequest`: 10 concurrent `Fetch` for one URL → server counts exactly 1 request.
  - `TestHeadersSent`: UA always present; `Accept-Encoding: gzip`; gzip body decoded; HWID headers present only when given.
  - `TestTimeout`: `New(20*time.Second)` is the production default; test uses 100 ms against a stalled server → `Problem=Network`.
  - `TestProblems`: 404 + `x-hwid-max-devices-reached` → `DeviceLimit`; 200 + empty body + `x-hwid-not-supported` → `HWIDRequired`; 200 HTML → `NotASubscription`; 200 empty → `EmptyBody`; 500 → `HTTPStatus`.
  - `TestNoSecretInError`: the error string for a failing URL `https://sub.example.com/sub/SECRETTOKEN` does not contain `SECRETTOKEN` (only the host).
  - `TestUAHelpers`: exact strings for the three UA builders with `("1.0.0","windows")` and `("1.0.0","macos")`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(subfetch): single-flight subscription fetcher`.

### Task 6: Profiles, subscriptions and the update merge; encrypted store

**Files:**
- Create: `internal/profile/profile.go`, `internal/profile/merge.go`, `internal/profile/*_test.go`, `internal/store/store.go`, `internal/store/keys.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: `link.Node`, `link.ID`, `subfetch.Result`, `ojson.Object`.
- Produces:
  - `type profile.Profile struct { ID, Name, SubscriptionID string; Node *link.Node; Config *ojson.Object; RoutingSource Source; Disabled bool }`; `type Source struct { Kind SourceKind; Preset Preset; Happ *happ.Profile }` with `SourceKind` `Provider|Preset|Happ` and `Preset` `AllProxy|AllExceptRU|SelectedOnly`.
  - `type profile.Subscription struct { ID, URL, Title, UA string; SendDevice bool; Info subhdr.Info; ProviderRouting *happ.Profile; LastGood time.Time; Profiles []Profile }`.
  - `profile.Apply(old Subscription, res subfetch.Result, now time.Time) (Subscription, Diff, error)`; `type Diff struct { Added, Removed, Changed []string }` (profile IDs). A result with `Problem != None` or zero usable profiles returns `old` unchanged and an error (spec §5.6). Profile IDs: `link.ID` for nodes; for JSON configs, hash of the proxy outbound's address/port/credential/transport.
  - `type store.KeyProvider interface { Key() ([]byte, error) }`; `store.FileKey(path string) KeyProvider` (0600 file, tests and Linux fallback; Windows DPAPI/Credential Manager and macOS Keychain providers are 1b); `store.Open(dir string, kp KeyProvider) (*Store, error)`; `(*Store).Load() ([]profile.Subscription, error)`; `(*Store).Save([]profile.Subscription) error` (AES-256-GCM, whole store encrypted, atomic rename, previous file kept as `.bak`; spec R3.5).

- [ ] **Step 1: Failing tests.**
  - `TestSelectionSurvivesUpdate`: same node reordered and its remark changed keeps its ID; selected ID still present (pitfall 46).
  - `TestBadUpdateKeepsOld`: `Problem=EmptyBody`, `Problem=DeviceLimit`, and a body of only skipped lines each return the old subscription byte-equal and an error.
  - `TestDiff`: one added, one removed, one changed (port) → exact Diff.
  - `TestRoutingHeaderApplied`: a links result with `Info.RoutingLink` set and enabled sets `ProviderRouting` and every profile's source Kind `Provider` uses it (spec §5.4 B4); for a JSON result the header is ignored.
  - `TestStoreRoundTrip` and `TestStoreEncrypted`: saved file contains none of the strings `REPLACE_ME`, the subscription URL host or a uuid; wrong key → error, `.bak` untouched; `TestStoreFlatSize`: 1000 saves of the same data keep file size constant (pitfall 49).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(profile,store): update merge and encrypted store`.

### Task 7: Config builder

**Files:**
- Create: `internal/gen/build.go`, `internal/gen/inbounds.go`, `internal/gen/rules.go`, `internal/gen/presets.go`, `internal/gen/*_test.go`, `testdata/golden/gen/*.json`

**Interfaces:**
- Consumes: `profile.Profile`, `happ.Translate`, `link.Outbound`, `ojson`.
- Produces:
  - `type gen.Ports struct { Mixed, Probe, DevTools, API int }` (chosen by 1b, persisted, spec R3.3).
  - `type gen.Input struct { Profile profile.Profile; Ports Ports; ProbeUser, ProbePass string; DevTools bool; AIDomains []string; UserBlocks []string; SelectedSites []string; BootstrapAllProxy bool; LogLevel string }`.
  - `gen.Build(in Input) (cfg []byte, report Report, err error)`; `type Report struct { ReplacedInbounds []string; ProxyTag string; Warnings []string }`.
  - `gen.AIDomains` default list: `domain:anthropic.com`, `domain:claude.ai`, `domain:claude.com`, `domain:openai.com`, `domain:chatgpt.com`, `domain:oaistatic.com`, `domain:oaiusercontent.com`, `full:generativelanguage.googleapis.com`, `domain:gemini.google.com`, `domain:aistudio.google.com`, `domain:cursor.sh`, `domain:cursor.com`.

- [ ] **Step 1: Golden tests** (one golden file each, compared as ordered JSON):
  - `TestBuildLinkAllProxy`: a vless reality node + preset AllProxy.
  - `TestBuildLinkAllExceptRU`: same node + preset AllExceptRU (direct: `geosite:category-ru`, `domain:ru`, `domain:su`, `domain:xn--p1ai`, `geoip:ru`, `geoip:private`; final proxy).
  - `TestBuildSelectedOnly`: AI domains + `SelectedSites` to proxy, final direct.
  - `TestBuildProviderJSON`: the finalmask fixture with source Provider: provider `routing.rules` appear after our prepended rules, in original order and content; `dns` byte-identical; provider inbounds replaced, their tags kept on our mixed inbound so provider `inboundTag` rules still match (Review Focus 4).
  - `TestBuildHappSource`: the legacy fixture's outbounds + Happ source = routing/dns from `happ.Translate`.
- [ ] **Step 2: Rule-order tests** (spec §6.3): `TestRuleLayers` asserts the exact prepended list for `DevTools=true`, `UserBlocks=["domain:bad.example"]`: (1) api inbound → api; probe inbound → `Report.ProxyTag` (the source's final outbound, or its balancer); (2) user blocks → block; (3) per-app (empty in 1a); (4) dev-tools inbound → proxy, then AI domains → proxy; (5) source rules; (6) source default. `TestBootstrapRule`: `BootstrapAllProxy` adds one prepended rule sending everything to proxy, after internal rules.
- [ ] **Step 3: Inbound tests.** `TestInbounds`: mixed on `127.0.0.1:Ports.Mixed` (`socks` + `http` via `mixed`, sniffing on), probe `socks` on `127.0.0.1:Ports.Probe` with `auth:"password"` and the given user/pass, dev-tools `mixed` on `Ports.DevTools` with tag `devtools-in`, `api` via `dokodemo-door` on `Ports.API` with `api.services:["HandlerService","StatsService","LoggerService"]`; nothing listens on non-loopback. `TestSerializerEscapes`: remark/password with `"`, `\`, U+2028 and Cyrillic produce valid JSON.
- [ ] **Step 4: Run** → FAIL. **Step 5: Implement** `Build` as a pipeline of pure functions (outbounds, inbounds, api/log, rule layers) over `ojson`. **Step 6: Run** → PASS. **Commit** `feat(gen): config builder with rule layers and presets`.

### Task 8: Geo merge

**Files:**
- Create: `internal/geo/merge/merge.go`, `internal/geo/merge/merge_test.go`

**Interfaces:**
- Consumes: `geo.NeededTags`, `geo/synth.Write` (tests), xray-core `common/geodata` protobuf types.
- Produces: `type merge.Source struct { Name, GeoIP, GeoSite string }` (paths); `merge.Merge(dstDir string, site, ip []string, primary, fallback Source) (Result, error)`; `type Result struct { GeoIP, GeoSite string; From map[string]string }` (tag → source name); `type MissingTagError struct { Tag, Kind string; Searched []string }` with message `geosite tag "win-spy" is missing from roscomvpn and v2fly`.

- [ ] **Step 1: Failing tests** with synthetic files: `TestPrimaryWins` (tag in both → primary entry); `TestFallbackFills`; `TestMissingTag` → `*MissingTagError` naming tag and both sources (Review Focus 5); `TestOutputLoadsInXray` (build tag `xrayint`): merged files pass `xray run -test` with a config using all tags; `TestAtomic`: an existing good output stays if merge fails midway; `TestNeedsOnlyRequested`: output contains only the requested tags.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** (write to temp files, fsync, rename). **Step 4: Run** → PASS. **Step 5: NOTICE.** Add xray-core `common/geodata` (MPL-2.0) and `google.golang.org/protobuf` (BSD-3-Clause) to `NOTICE` (final review M8). **Commit** `feat(geo): merged geo files per tag set`.

### Task 9: Validation

**Files:**
- Create: `internal/validate/schema.go`, `internal/validate/xraytest.go`, `internal/validate/*_test.go`

**Interfaces:**
- Produces: `validate.Schema(cfg []byte) (warnings []string, err error)` — walks the JSON against xray-core `infra/conf` Go types by reflection (`json` tags, embedded structs, `json.RawMessage` fields resolved per protocol/network: outbound `settings` by `protocol`, `streamSettings.*Settings` by `network`/`security`); unknown key → warning naming the JSON path; wrong type → error. `validate.XrayTest(ctx, xrayPath, cfg []byte, assetDir string) error` — runs `xray run -test -config stdin:` with `XRAY_LOCATION_ASSET=assetDir`, error includes Xray's message minus secrets.

- [ ] **Step 1: Failing tests:** `TestSchemaFixturesClean` (both fixtures and every gen golden: zero warnings, nil error); `TestUnknownKey` (`outbounds[0].settings.adress` → warning `outbounds[0].settings.adress: unknown key`); `TestWrongType` (`port:"x"` → error); `TestXrayTestRejects` (`xrayint` tag: a config with an unknown outbound protocol fails with Xray's message).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(validate): schema check and xray -test`.

### Task 10: Xray runner and latency test

**Files:**
- Create: `internal/xrayrun/run.go`, `internal/xrayrun/latency.go`, `internal/xrayrun/*_test.go`

**Interfaces:**
- Consumes: `gen.Build`, `validate.XrayTest`.
- Produces:
  - `type xrayrun.Runner struct` with `xrayrun.New(xrayPath, assetDir string, clock Clock) *Runner`; `(*Runner).Start(ctx, cfg []byte, ready ReadyCheck) error` (config on stdin, never on disk, spec R3.5); `(*Runner).Stop(timeout time.Duration) error`; `(*Runner).Events() <-chan Event` with `Event{Kind: Started|Exited|Restarting|GaveUp, Err error, At time.Time}`; restart with backoff 1, 2, 4, 8, 16, 30 s capped, giving up after 5 crashes within 2 minutes; `ReadyCheck` dials the mixed inbound until it accepts (max 10 s).
  - `xrayrun.Latency(ctx, xrayPath, assetDir string, outbounds []*ojson.Object, url string, timeout time.Duration) []LatencyResult` — one temporary Xray with one authenticated socks inbound per outbound, an HTTP GET of `url` (default `https://www.gstatic.com/generate_204`) through each; `LatencyResult{Tag string; RTT time.Duration; Err error}` (spec brief 5.1: through the core, not ICMP).
  - `xrayrun.Clock` interface (`Now`, `After`) for tests.

- [ ] **Step 1: Unit tests** with a fake "xray" (a Go test helper binary via `os.Args[0]` re-exec): `TestConfigViaStdin` (helper echoes stdin to a file in the temp dir; equals cfg; no other file written); `TestBackoff` (fake clock; crashes at t=0,1,3,7,15 → Restarting delays 1,2,4,8 then GaveUp on the 5th within 2 min); `TestStopKillsOnlyOurChild` (stops by the PID it started, no name lookup).
- [ ] **Step 2: Integration test** (`xrayint`): `TestLatencyLocal` against two local freedom-backed socks servers (one up, one closed port) → one RTT, one error.
- [ ] **Step 3: Run** → FAIL. **Step 4: Implement.** **Step 5: Run** → PASS. **Commit** `feat(xrayrun): supervised Xray runner and core latency test`.

### Task 11: End-to-end pipeline against local servers (stage 1 protocol acceptance in CI)

**Files:**
- Create: `internal/e2e/e2e_test.go` (build tag `xrayint`), `internal/e2e/servers.go`, `testdata/e2e/server-*.json` (templates with placeholders filled at test time)
- Modify: `.github/workflows/ci.yml` (run `./internal/e2e/...` in the existing `xray-integration` job on Linux and Windows)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Test servers.** At test time generate fresh keys with the pinned binary (`xray x25519`, `xray uuid`, self-signed cert via `crypto/x509` for Hysteria2 TLS) and start three local Xray servers on random ports: VLESS Reality + `xtls-rprx-vision` (target `127.0.0.1:<local https echo>`), VLESS XHTTP (`mode: packet-up`) over TLS, Hysteria2 with salamander + the fixture's `exp` noise in `finalmask`. A local HTTP echo server answers `GET /ip` with the remote address.
- [ ] **Step 2: Failing tests.** For each server: build a share link (or for Hysteria2-DNS, the finalmask JSON fixture with host/port/secrets replaced), run `subbody.Decode` → `profile.Apply` → `gen.Build` (preset AllProxy) → `geo/merge` with synthetic geo → `validate.Schema` + `XrayTest` → `xrayrun.Start`; then an HTTP GET through the mixed inbound to the echo server succeeds and the echo sees the server's outbound address. Names: `TestE2EVLESSRealityVision`, `TestE2EVLESSXHTTP`, `TestE2EHysteria2DNS`. Also `TestE2ELegacyRunsAsIs`: the legacy fixture (noises via dialerProxy) connects to the Hysteria2 server (spec §6.4).
- [ ] **Step 3: Run** `GOTOOLCHAIN=go1.27.4 go test -tags xrayint ./internal/e2e/...` with `ZATVOR_XRAY` set as in `internal/xrayint` → FAIL first, then implement helpers until PASS.
- [ ] **Step 4: CI.** Add `./internal/e2e/...` to the `xray-integration` matrix; push; both OS green. **Commit** `test(e2e): pipeline connects over Reality+Vision, XHTTP and Hysteria2`.

### Task 12: Docs and stage 1a wrap-up

**Files:**
- Modify: `docs/STATUS.md`, `CHANGELOG.md`, `docs/decisions/README.md` (+ new ADRs if any decision changed), `README.md` ("What Zatvor sends": UA, HWID, device headers, switches)
- Create: `docs/checklists/stage-1a.md`

- [ ] **Step 1:** STATUS: 1a done, 1b next; list deferred items. CHANGELOG entries per package. "What Zatvor sends" section in README (ru + en).
- [ ] **Step 2:** Checklist for Artur: nothing to run by hand in 1a; link the green CI runs of `xray-integration` with the three E2E tests.
- [ ] **Step 3: Commit** `docs: stage 1a status, changelog and checklist`.

---

## Next plans (outline only, written after 1a)

- **1b zatvord on Windows:** state model and local API (ADR 0009: token, Host/Origin, discovery file, SSE), single instance, journal with fsync and revert on start, Windows system proxy (HKCU + `InternetSetOption`, conflict warning), port picker with excluded ranges (pitfall 57), key providers (DPAPI + Credential Manager), scheduled updates with backoff 1→2→4… min capped at the interval and only through the tunnel (R3.6), geo bootstrap and refresh, dev-tool routing (§9: shell profiles, `~/.claude/settings.json`, VS Code, wrappers; all journaled), supervisor, diagnostics bundle (secrets redacted), CLI `status`/`check` with exit codes 0/1/2/3. System changes run only under `systemtest` on CI Windows runners or on Artur's PC in safe mode.
- **1c UI and delivery:** Tauri tray and popup (from the prototype, styles as classes), panel basics (overview, profiles and subscriptions, settings, about), first run (paste link → connect), ru/en, light/dark, Happ hint card (§5.3), update diff, MSI (WiX v5), stage 1 acceptance with Artur: test subscription imports; Hysteria2-DNS, XHTTP and Vision connect from his home internet.
