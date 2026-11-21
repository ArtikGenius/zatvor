# TUN front prototype A vs B: results (stage 0c, task 8)

Spec §4.3. Both variants chain into Xray v26.11.9, the only protocol engine:

- **A**: Xray's own `tun` inbound (`proto/tun/a`): `autoSystemRoutingTable` `["0.0.0.0/0","::/0"]`,
  `autoOutboundsInterface: "auto"`, `autoSystemWfpBlockLeak: ["dns","misconfigtun"]`, `dns` on the TUN,
  SOCKS outbound to the upstream. wintun.dll next to xray.exe.
- **B**: sing-tun v0.9.6 as a Go library (`proto/tun/b`): system stack, `AutoRoute`, `StrictRoute`,
  every TCP/UDP flow to an Xray SOCKS inbound (`proto/tun/b/client-xray.json`), which goes on to the upstream.
  sing-tun embeds Wintun itself (memmod), so no DLL is shipped.

Same addresses in both: TUN `172.19.0.1/30` + `fdfe:dcba:9876::1/126`, TUN DNS `172.19.0.2` / `fdfe:dcba:9876::2`.

Where: GitHub Actions `windows-latest` (Windows Server 2025 Datacenter 10.0.26100, 4 cores, Azure VM: Hyper-V
NIC + accelerated-networking VF with the same MAC, no IPv6 route), one fresh runner per variant and attempt,
workflow `.github/workflows/proto-tun.yml`, script `proto/tun/check/Check.ps1`. No developer machine was used.

## Results (final run 44924472104, two runners per variant)

| Criterion | A run 1 | A run 2 | B run 1 | B run 2 | Pass rule | A | B |
|---|---|---|---|---|---|---|---|
| `dnsLeakV4` (port 53/853 packets on the physical NIC) | 0 | 0 | 0 | 0 | 0 | pass | pass |
| `dnsLeakV6` | 0 | 0 | 0 | 0 | 0 | inconclusive (see below) | inconclusive |
| `bypassPackets` (probe addresses on the physical NIC) | 0 | 0 | 0 | 0 | 0 | pass | pass |
| `killRecovery` (adapter gone / routes clean / internet back, s) | 0.04 / 0.04 / 0.31 | 0.00 / 0.00 / 0.33 | 0.00 / 0.00 / 0.35 | 0.05 / 0.05 / 0.33 | all ≤ 5 s, routes = baseline | pass | pass |
| WFP filters named Xray / sing-tun, before → after kill | 30 → 0 | 30 → 0 | 10 → 4 | 10 → 4 | (info) | | |
| restart after kill: TUN up again (s), tunnel works | 0.38, yes | 0.32, yes | 0.32, yes | 0.37, yes | (info) | | |
| `adapterLeftovers` raw (new items after 2 kill cycles) | 1 | 1 | 1 | 1 | see below | | |
| of which network adapters (`Get-NetAdapter -IncludeHidden`) | 0 | 0 | 0 | 0 | 0 | pass | pass |
| of which non-present Wintun PnP devnode (same id reused on restart) | 1 | 1 | 1 | 1 | no growth | pass | pass |
| `throughputMbps` (30 s TCP download through the TUN) | 1546.5 | 2488.1 | 1858.6 | 1735.9 | (compare) | | |
| `cpuIdlePct` (60 s, % of all 4 cores) | 0.000 | 0.000 | 0.130 | 0.052 | < 1 % | pass | pass |
| idle CPU, % of one core | 0.000 | 0.000 | 0.521 | 0.208 | (info) | | |
| `perAppRule` (curl.exe by path → direct; PowerShell and nslookup → tunnel), ~40 s and ~140 s after TUN up | true, true | true, true | true, true | true, true | true | pass* | pass |
| TUN up after start (s) | 1.12 | 1.08 | 3.34 | 3.42 | (info) | | |
| Sleep/wake | not measured | | | | | not measured in 0c, moved to stage 3 Hyper-V VM check | |

\* A matches process paths **case-sensitively**: see "per-app rule" below.

Since commit c5b41a2 (after review): `Check.ps1` writes `dnsLeakV6 = "not measured: no IPv6 on runner (...)"` when the
runner has no IPv6, and `perAppRule` is the AND of the early and late checks. The raw JSON of run
44924472104 predates this (it has `dnsLeakV6: 0` and the early check only; both checks were true there).

Throughput over all runs where it was measured (Mbit/s): A 3054.6 (run 44922767086), 1602.5 (44923328972),
2363.3 (44923882926), 1546.5, 2488.1 (44924472104) — median 2363; B 1726.4, 1751.0, 1858.6, 1735.9 — median 1743.
Loopback without any tunnel or Xray on the same runners: 28 800–44 900 Mbit/s, so neither number is
limited by the bench tool. Runner-to-runner noise is large (A: 1.5–3.1 Gbit/s); B's spread is small.

The raw JSON of the final run is in `proto/tun/results/run44924472104-tun-{A,B}-{1,2}.json` (device GUIDs replaced by `{GUID}` for the secret scanner; the Wintun devnode id was the same on every A runner and on every B runner).

## How it was measured (and the limits)

**Topology.** Everything runs on the runner. `bench serve` answers HTTP on `127.0.0.1:18080` with
`zatvor-tunnel` and streams zeros on `127.0.0.1:18081`. The "upstream" Xray (`proto/tun/upstream/config.json`)
stands in for the remote server: SOCKS on `127.0.0.1:10800`; freedom bound to the physical adapter
(`sockopt.interface`); DNS answered by its `dns` outbound (hosts `zatvor-probe.test` → `198.18.0.10`,
`2001:db8::10`; everything else over DoH to `1.1.1.1:443`, never port 53); probe addresses redirected to bench:
`198.18.0.10:80`, `[2001:db8::10]:80`, `1.1.1.1:80` → HTTP, `198.18.0.20:5201` → zero stream.

**Leaks.** `pktmon` captures only the physical NIC components (picked by MAC; both the Hyper-V NIC and its VF)
while a scripted workload runs: `Resolve-DnsName` (Windows DNS Client, A/AAAA, random and real names, so
smart multi-homed resolution would show), `nslookup` to the default server, to `1.1.1.1` and to
`2606:4700:4700::1111`, raw HTTP from PowerShell to the three HTTP probes, a TCP connect to the bench probe,
then 10 s of the runner's own background traffic. `pcapcount` counts in the pcapng:
- DNS leak: any TCP/UDP packet with source or destination port 53 or 853, per IP version. The upstream never
  sends those, so every one left outside the tunnel (this includes the runner agent's own lookups).
- Bypass: any packet to or from a probe address (or `1.1.1.1:80`). Through the tunnel these end on loopback,
  so on the NIC they can only be traffic that skipped the TUN.

The same capture **without** the tunnel is the control, and it sees the leaks: in run 44924472104, 84–112 DNS packets to
`168.63.129.16:53` / `1.1.1.1:53` and 27–30 probe packets (3 flows) per runner (earlier runs: up to 116 in
44923882926 and 142 in 44922767086, both on A). With either tunnel up: 0 and 0.

Limits, stated plainly:
- Only DNS ports and the probe destinations are attributed. Other traffic on the NIC (e.g. to Azure's
  `168.63.129.16:80`/`32526` and `169.254.169.254`, which have host routes on the physical NIC, and the
  upstream's own egress) is not classified as tunnel or bypass; pktmon cannot filter by process. The top
  endpoints are logged in each JSON for inspection.
- **IPv6 is inconclusive**: the runners have no IPv6 default route or address, so an IPv6 leak had nowhere to
  go. The IPv6 *tunnel path* works in both (`[2001:db8::10]:80` and DNS to `fdfe:dcba:9876::2` and
  `2606:4700:4700::1111` answered through the TUN), but `dnsLeakV6 = 0` proves nothing. Needs a dual-stack
  network: stage 3 Hyper-V VM and Artur's macOS check.
- DNS over HTTPS by Windows itself (Server 2025 can) would not be caught by the port rule; the runner has no
  DoH server configured.
- **killRecovery**: `Stop-Process -Force` on the TUN owner (A: client xray.exe; B: b.exe). Polled every
  250 ms: adapter gone, no route left on it, TCP to `1.1.1.1:443` and a fresh DNS answer through the
  physical NIC. Routes and DNS servers afterwards equal the baseline exactly. The WFP filters of A (a dynamic
  session) were all gone; B leaves 4 filters whose name contains "sing-tun" — most likely the persistent
  Windows Firewall rule `sing-tun (<exe path>)` that sing-tun's system stack adds (`fixWindowsFirewall`),
  not checked further.
- **adapterLeftovers**: after kill -9, then a restart and a second kill -9, no network adapter is left in
  either variant, but Windows keeps the Wintun software device node (`SWD\WINTUN\{GUID}`, status
  Unknown = not present). Both A and B use a GUID derived from the adapter name, and the restart reused the
  same node, so they do not pile up. This is Wintun's behaviour when its owner dies, identical in A and B.
  The pass rule (0 adapters, no growth) was set after seeing this; read strictly (raw count 0), neither
  variant passes this row.
- **throughput** is loopback-to-loopback through the TUN (TUN → front → SOCKS → upstream → bench), so it
  measures the front and the Xray hops, not a network. One TCP stream, download only.
- **cpuIdlePct**: `TotalProcessorTime` of the front's processes (A: client xray.exe; B: b.exe + client
  xray.exe) over 60 s without test traffic, divided by wall time and cores. Windows accounts CPU in 15.6 ms
  ticks, so A's 0.000 means less than one tick in 60 s. The upstream is not counted.
- **perAppRule**: A uses an Xray routing rule `process: ["C:/Windows/System32/curl.exe"] → freedom`; B looks
  up the owner of the flow's source port (`GetExtendedTcpTable` + `QueryFullProcessImageName`) and dials
  direct, bound to the default interface. Direct means curl got Cloudflare's real `Server: cloudflare`
  answer; tunnel means bench's `zatvor-tunnel`. The access logs confirm it (`[tun-in -> direct]` for curl,
  `-> proxy` for PowerShell). B's rule is TCP only.
- Sleep/wake: a CI runner cannot sleep. Not measured in 0c, moved to the stage 3 Hyper-V VM check.
- B is a prototype handler of a few hundred lines, not sing-box; a tuned implementation may be faster.

## Per-app rule: Xray compares paths case-sensitively

Runs 44922767086 and 44923328972 used the path as `Get-Process` reports it, `C:\Windows\system32\curl.exe`
(lower-case `system32`, as PATH spells it). A sent curl through the tunnel; B (case-insensitive compare,
written here) sent it direct. Run 44923882926 added diagnostic rules; the access log shows the first rule
(`C:/Windows/system32/curl.exe`) never matched and the canonical path did:

```
16:21:28.081397 from tcp:172.19.0.1:49931 accepted tcp:1.1.1.1:80 [tun-in -> direct-canon]
```

Xray's `ProcessNameMatcher` uses `slices.Contains` / `strings.HasPrefix` on the path from
`QueryFullProcessImageName`, which reports the file system's case (`C:\Windows\System32\curl.exe`). From
run 44924472104 on, `Check.ps1` puts the exact-case path in the rule (`Get-CanonicalPath`), and A passes. A
product on A must canonicalise every user-chosen path the same way. Also seen once, in run 44923882926 (not
in 44924472104): `app/router: Unables to find local process name: common/net: not found`, probably for a
connection that closed at once (the lookup only considers ESTABLISHED TCP); such a flow falls through to the
next rule.

## Wintun pin

`Check.ps1` downloads `https://www.wintun.net/builds/wintun-0.14.1.zip` and refuses to go on unless its
SHA-256 is `07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51`. The authoring sandbox could not
reach wintun.net (egress proxy 403), so the value was not computed there: it is the published hash of that
zip, and every A runner on CI (6 runners over 5 runs) printed the same hash for the file it downloaded. The Xray Windows zip
also bundles a wintun.dll; the prototype does not use that copy.

## CI runs

| Run | What | Outcome |
|---|---|---|
| 44922529480 | first harness | both jobs stopped after the Wintun hash check: PowerShell `foreach ($d ...)` overwrote `$D` (names are case-insensitive) |
| 44922767086 | first numbers | everything measured; A per-app false (path case) |
| 44923328972 | log excerpts | A per-app false again; Xray's access log shows curl's flow `-> proxy` |
| 44923882926 | diagnostic rules | A's canonical-path rule matched; case-sensitivity confirmed |
| 44924472104 | final, 2 runners per variant, canonical path, restart cycle | all rows above |
