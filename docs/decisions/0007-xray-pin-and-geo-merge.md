# 0007: Xray pin, verified fetch and geo database merge

Status: accepted, 2026-11-21 (stage 0c, Tasks 2, 3, 5). The runtime geo merge is implemented in stage 1.

## Context

Zatvor must run a known Xray build, never "latest", and must resolve geo tags from two databases with
different contents: roscomvpn (Russia-oriented, what Happ profiles expect) and v2fly (broad coverage). Xray
loads one `geoip.dat` and one `geosite.dat`, so a collision between the two sets must be resolved before
Xray starts (spec §6.5). Background: `docs/research/xray-core.md`.

## Decision

1. **Pin Xray v26.11.9.** Xray's v26 tags are not valid Go module versions, so the Go dependency uses the
   pseudo-version for commit `676eaf6` (the commit of tag v26.11.9, checked in Task 2). The pin lives in
   `pin.json`.
2. **Verified fetch** in `internal/xraybin` (thin CLI `tools/xrayfetch`): download the release archive,
   verify the `.dgst` digest against the pin, reject unsafe archive paths, extract. CI runs the real binary
   (`internal/xrayint`, including `xray -test` on generated configs) on Linux and Windows.
3. **Synthetic geo files for tests** (`internal/geo/synth`): tiny `geoip.dat`/`geosite.dat` built from the
   tags a config needs (`internal/geo`, `NeededTags`), so tests run offline and without the multi-MB
   databases.
4. **Runtime merge, roscomvpn wins.** At config build time zatvord computes the tags each config needs and
   writes a merged `geoip.dat`/`geosite.dat` per tag set: roscomvpn entries win on name collisions, v2fly
   fills the rest. Updates arrive through the tunnel with SHA-256 verification and atomic swap, with
   rollback to the previous set if Xray fails to start. An unknown tag is a human error that names the tag
   and the database missing it, raised before Xray starts. Implementation is stage 1.

## Options rejected

- Rewriting provider configs to `ext:file.dat:tag`: changes what the provider sent.
- Using `latest` or a version range: unverifiable and not reproducible.

## Consequences

- Bumping Xray is a deliberate change of `pin.json` plus a green integration run.
- Stage 1 needs the merger, tag scanner on real configs, and the update/rollback path.
- Bundled geo files remain the fallback while a download is pending (spec §6.5).
