# 0003: TUN front

Status: accepted, 2026-11-21 (stage 0c, Task 8). Revisit if the stage 3 checks below fail.

## Context

Zatvor needs a TUN front on Windows (and later macOS) that feeds Xray v26.11.9, the only protocol engine
(spec §4.3). Two candidates were prototyped in `proto/tun` and measured on disposable GitHub Actions Windows
runners (`.github/workflows/proto-tun.yml`, `proto/tun/check/Check.ps1`). The spec's rule: **A only if it
passes every measured row; otherwise B** (the default expectation was B: Xray's TUN stack is young, its
Windows IPv6/DNS fixes were still landing in November, see `docs/research/xray-core.md` §5).

## Options

- **A. Xray's own `tun` inbound**: one process. Config keys checked in the v26.11.9 source
  (`infra/conf/tun.go`): `gateway`, `dns`, `autoSystemRoutingTable`, `autoOutboundsInterface`,
  `autoSystemWfpBlockLeak` (`["dns","misconfigtun"]`). Needs wintun.dll next to xray.exe.
- **B. sing-tun v0.9.6 (GPL-3.0-or-later) inside a Zatvor helper**, system stack with auto route and strict
  route, forwarding every flow to an Xray SOCKS inbound (the v2rayN default). Embeds Wintun. One more
  process and hop; the per-app logic is ours.

## Measurements

Final run 44924472104, two fresh runners per variant (Windows Server 2025, 4 cores, no IPv6 on the runner).
Method, limits and earlier runs: `proto/tun/RESULTS.md`; raw JSON: `proto/tun/results/`.

| Criterion | A (run 1 / run 2) | B (run 1 / run 2) | A | B |
|---|---|---|---|---|
| DNS leak IPv4, packets on the physical NIC (control without tunnel, run 44924472104: 84–112) | 0 / 0 | 0 / 0 | pass | pass |
| DNS leak IPv6 | 0 / 0, no IPv6 on runner | 0 / 0, no IPv6 on runner | inconclusive | inconclusive |
| Bypass packets to probe addresses (control: 27–30) | 0 / 0 | 0 / 0 | pass | pass |
| kill -9: adapter gone / routes clean / internet back (s) | 0.04/0.04/0.31, 0.00/0.00/0.33 | 0.00/0.00/0.35, 0.05/0.05/0.33 | pass | pass |
| Adapter leftovers after 2 kill cycles: adapters / non-present Wintun devnode (reused) | 0 / 1, 0 / 1 | 0 / 1, 0 / 1 | pass | pass |
| Throughput through the TUN, Mbit/s (all runs: median A 2363, B 1743) | 1546.5 / 2488.1 | 1858.6 / 1735.9 | higher | lower |
| CPU idle, % of all cores over 60 s | 0.000 / 0.000 | 0.130 / 0.052 | pass | pass |
| Per-app rule by process path (curl.exe direct, others tunnel) | true / true | true / true | pass (exact-case path) | pass |
| Sleep/wake | not measured in 0c | not measured in 0c | moved to stage 3 | moved to stage 3 |

## Decision

**A: Xray's own `tun` inbound**, by the spec rule: A passed every row that a CI runner can measure, with
higher median throughput (per-run ranges overlap) and lower idle CPU than B, in one process instead of two. B stays in `proto/tun/b` as the
fallback.

The decision rests on two readings that a reviewer should see:
- **Adapter leftovers**: after `kill -9` no network adapter is left in either variant, but Windows keeps a
  non-present Wintun software devnode (`SWD\WINTUN\{GUID}`), the same id on every start, so nothing piles
  up. The rule "0 adapters, no growth" was set after seeing this. It is Wintun's behaviour and identical in
  A and B. Read strictly (no new PnP node at all), neither variant passes, A would fail, and the spec rule
  would give B by default without any difference between the two.
- **Per-app rule**: Xray matches process paths case-sensitively. The rule works with the path as Windows
  reports it (`C:\Windows\System32\curl.exe`) and fails with another spelling of the same file
  (`...\system32\...`, as `Get-Process` and PATH give it).

## Consequences

- Zatvor ships xray.exe with wintun.dll (pinned: wintun-0.14.1.zip, SHA-256
  `07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51`). Wintun's prebuilt-binary licence must be
  checked before the first release (`NOTICE`).
- The Xray config generator owns the TUN settings above. `autoSystemWfpBlockLeak` (`dns`, `misconfigtun`;
  `all`/`allexceptlan` to be evaluated) is **in-session leak protection only, not a kill-switch**: its
  filters live in a dynamic WFP session of xray.exe, so when Xray dies Windows removes them with the
  adapter and routes (measured: 30 filters → 0, traffic leaves directly after ~0.3 s). Staying fail-closed
  across an Xray crash needs persistent WFP block filters owned by zatvor-helper / the protection engine
  (spec §4.1 assigns WFP blocks to the helper), which only the helper removes. B fails open the same way:
  after kill, internet was back directly in 0.33–0.35 s and its WFP filters went 10 → 4 (the 4 left are
  most likely sing-tun's persistent firewall rule, not blocks; sing-tun v0.9.6 opens its strict-route
  session with `FWPM_SESSION_FLAG_DYNAMIC`, `tun_windows.go:197`), so this does not change the decision. The
  daemon supervises and restarts xray.exe: in the restart-after-kill step the TUN was up again 0.32–0.38 s
  after the new xray.exe started (A), against 1.08–1.12 s for the first start of a fresh runner.
- Every process path in per-app rules is canonicalised to the file system's exact case (the same API Xray
  uses, `QueryFullProcessImageName`, or a case-correct path walk) before it goes into the config. A test pins
  this. Rules on flows whose owner Xray cannot find (seen once in run 44923882926, probably a connection that closed at once) fall
  through to the next rule, so "send app X direct" fails safe into the tunnel, but "send app X to block"
  would not: per-app *block* rules need their own check before they are offered.
- Must still be checked, because a runner cannot: IPv6 leaks on a dual-stack network and sleep/wake (stage 3
  Hyper-V VM), and macOS (Artur). If A fails any of them, switch to B: the prototype and the harness are
  ready to re-run (`gh workflow run proto-tun.yml`).
- Xray's TUN is younger than sing-tun (research §5: open issues #7160, #6535). Pin Xray versions and re-run
  `proto-tun` on every Xray update.
- sing-tun and sing (GPL-3.0-or-later) stay prototype-only dependencies in the separate `proto/tun` Go
  module; the root module does not depend on them.
