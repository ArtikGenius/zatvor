# 0006: Installer format and one-line install script

Status: accepted, 2026-11-21 (stage 0c, Task 10).

## Context

Spec §16 Q2.9: Zatvor installs machine-wide into Program Files from the first release, with one admin prompt.
In stage 3 the same installer adds the privileged helper service (§4.1 zatvor-helper). The updater runs
elevated only for the replace step.

Spec §11: `SHA256SUMS` is signed with Ed25519. The app, the helper and the install scripts all verify that
signature. R3.2 adds two requirements:

- `install.ps1` verifies Ed25519 with a compact implementation on `System.Numerics.BigInteger`, because
  Windows PowerShell 5.1 has no Ed25519.
- On macOS and Linux, the script uses `openssl` and refuses, with instructions, when `openssl` lacks Ed25519.

The candidates for the Windows installer were WiX/MSI and Inno Setup (§13 item 6). Both were built and
measured only on GitHub Actions (`.github/workflows/proto-installer.yml`). Method and raw data:
`proto/installer/RESULTS.md`, `proto/installer/results/`.

## Options

- **Inno Setup 6.7.1**: a single `setup.exe` with a wizard UI and Pascal scripting. Services are not
  declarative: they need `[Run]` calls to `sc.exe` or `[Code]`, and these have no rollback. The compiler
  runs only on Windows.
- **WiX Toolset 5.0.2**: Windows Installer (MSI), authored in XML and built by a .NET global tool.
  `ServiceInstall` and `ServiceControl` are declarative, and Windows Installer rolls them back as part of
  the transaction. `MajorUpgrade` handles upgrades. `msiexec` is the standard for silent and managed
  deployment. The default UI is minimal; a wizard needs the WixUI extension. Building the MSI also needs
  Windows.

## Measurements

Runs 44945146447 and 44945485474 on windows-latest (Windows Server 2025, 10.0.26100), with a dummy payload
`zatvor.exe` (cmd/zatvord, 1,746,432 bytes) plus `VERSION.txt`.

| Criterion | Inno Setup | WiX MSI |
|---|---|---|
| Installer size | 2,705,251 / 2,705,196 B (2.58 MiB) | 700,416 B (0.67 MiB) |
| Admin prompts | not measured: the runner is elevated and has no UAC. Expected 1 (manifest `asInvoker`, self-elevation from `PrivilegesRequired=admin`) | not measured: same reason. Expected 1 (`ALLUSERS=1`, Word Count 2 = elevation required) |
| Silent install / uninstall | `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART` | `msiexec /i … /qn /norestart`, `msiexec /x {ProductCode} /qn` |
| Upgrade v0.0.1 → v0.0.2 in place | yes, 1 ARP entry | yes, 1 ARP entry |
| Leftovers after uninstall (dir, ARP, Start menu, HKLM key) | none | none |
| Files installed besides the app | `unins000.exe`, `unins000.dat` | none |
| Install / upgrade / uninstall time | 1–2 s each; this is the wait's polling granularity, so no difference between the formats can be seen | same |

Install scripts, from the same runs:

- **Ed25519 verify in pure PowerShell.** It passes RFC 8032 §7.1 TEST 1–3 and rejects a flipped message bit,
  `S ≥ L`, and non-canonical A or R (11 Pester tests, Pester 5.9.0, on PS 5.1.26100 and 7.6.10). Verify time
  on 5.1 over four runs: median 61–140 ms, worst first call 321 ms. Target < 2 s: met.
- **install.ps1 end to end**, with a throwaway CI key, on 5.1 (Inno and MSI) and 7.6 (Inno). All of these
  pass: it refuses without `CI=true` (exit 2); it does not accept the placeholder key; it rejects a flipped
  signature bit, a changed `SHA256SUMS` and a changed installer (exit 1, nothing installed); a valid release
  installs silently and uninstalls with no leftovers.
- **install.sh.** OpenSSL 3.5.5 (ubuntu) and Homebrew OpenSSL 4.1.0 (macOS PATH default) verify. macOS
  `/usr/bin/openssl` (LibreSSL 3.3.6) is refused with "brew install openssl@3…" (exit 3), and a PATH shim
  without Ed25519 is refused too.

## Decision

**WiX v5 / MSI** is the Windows installer format. The script stays as built: `install.ps1` with an embedded
BigInteger Ed25519 verifier, and `install.sh` on `openssl pkeyutl -verify -rawin` with a capability probe.

On the measured criteria the two formats tie: one expected prompt each, silent flags, upgrade in place
with one ARP entry, no leftovers, seconds to install. Where they differ, MSI is ahead:

- it is a quarter of the size (0.67 vs 2.58 MiB for the same payload);
- it installs nothing but the app;
- stage 3's helper service gets declarative install, stop, start and removal, with transactional rollback
  on a failed install or upgrade. Inno would need `sc.exe` calls without rollback. This matters most for a
  service that controls routing and DNS.

The service part is an argument from the formats' design. Stage 0c did not build a service.

## Consequences

- Stage 1 release: build `zatvor-<ver>.msi` with WiX 5.0.2 (pinned) on a Windows runner. Keep `UpgradeCode`
  `F1EE344A-8F54-414D-96AD-5D33B283DC37` fixed forever; it is what makes upgrades in place work. Version
  changes must touch the first three fields, because `MajorUpgrade` ignores the fourth. Sign the MSI
  (Authenticode) once there is a certificate; that was not covered here.
- Installer UI: the MSI has only the basic msiexec progress dialog. If a wizard is wanted, add the WixUI
  extension, or a small bootstrapper later. The normal path is the one-line script or the app's updater.
- Elevation: `msiexec /qn` cannot show a UAC prompt. If it is unelevated, the install fails instead
  (documented Windows Installer behaviour; not measured, because the runner is elevated). So `install.ps1`
  and the updater start msiexec with `-Verb RunAs`, which gives the single prompt. `install.ps1` already
  does this.
- `install.ps1`:
  - The embedded key is the labelled throwaway "TEST KEY -- replaced in stage 1 by Artur's release key"; its
    private half was discarded. Stage 1 swaps in the real key, and `install.sh` gets the PEM of the same key.
  - Stage 1 replaces `-Source` (local directory) with downloads from the GitHub release. The order stays:
    signature, then hash, then installer, all on private temp copies.
  - The `CI=true` gate is a stage 0c rule and goes when a real release exists.
  - The one-liner form is `& ([scriptblock]::Create((irm <url>))) <args>`, because `irm | iex` cannot pass
    parameters.
  - `Ed25519.ps1` stays the source. CI checks that the copy inside `install.ps1` is identical.
- The Inno-specific branch (`/VERYSILENT` for `.exe`) in `install.ps1` can be removed in stage 1, along
  with `proto/installer/inno`.
- macOS/Linux: `install.sh` needs OpenSSL 3 or newer. Stock macOS (LibreSSL) is refused with brew
  instructions, by design. There is no macOS/Linux package yet; `install.sh` stops after verification.
