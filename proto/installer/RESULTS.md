# Stage 0c, task 10: installer format and one-line install script

Throwaway installer prototypes; the install scripts in `scripts/install/` carry into later stages.
Decision: `docs/decisions/0006-installer-and-install-script.md`. Nothing here ran on a developer machine:
every number comes from GitHub Actions (`.github/workflows/proto-installer.yml`). Installs and uninstalls
ran only on disposable `windows-latest` runners. The scripts that change the system refuse unless `CI=true`.

## What was built

| | Inno Setup (`inno/zatvor.iss`) | WiX (`wix/Package.wxs`) |
|---|---|---|
| Tool version | Inno Setup 6.7.1 (preinstalled on the runner image; pinned with `choco install innosetup --version 6.7.1`, which was a no-op) | WiX Toolset 5.0.2 (`dotnet tool install --global wix --version 5.0.2`, `DOTNET_ROLL_FORWARD=Major`) |
| Output | `zatvor-setup-<ver>.exe` | `zatvor-<ver>.msi` |
| Scope | `PrivilegesRequired=admin`, `{autopf}\Zatvor` = `C:\Program Files\Zatvor` | `Scope="perMachine"`, `ProgramFiles64Folder\Zatvor` |
| Identity | fixed `AppId` | fixed `UpgradeCode` + `MajorUpgrade` |
| Contents | `zatvor.exe` + `VERSION.txt`, Start menu shortcut | the same, plus the `HKLM\Software\Zatvor` key that holds the shortcut component's key path |
| Stage 3 service | commented `[Run]`/`[UninstallRun]` `sc.exe` sketch | commented `ServiceInstall`/`ServiceControl` sketch |

The dummy `zatvor.exe` is `cmd/zatvord` built for windows-amd64 (`-trimpath -ldflags="-s -w"`, 1,746,432 bytes).
It only prints its version. `VERSION.txt` holds `0.0.1` or `0.0.2`, so the upgrade check can see which build
is installed (zatvord's version is a constant).

## Method

`Measure.ps1` runs under Windows PowerShell 5.1 for each format, and does these steps in order:

1. It checks that nothing Zatvor-related is present.
2. It records the installer size and the elevation evidence: for Inno, the manifest's `requestedExecutionLevel`; for the MSI, the Summary Information Word Count and the `ALLUSERS` property.
3. It installs v0.0.1 silently and records the state.
4. It installs v0.0.2 over it and records the state.
5. It uninstalls silently through the "Apps & features" entry and records what is left.

"State" means: the `C:\Program Files\Zatvor` files, `VERSION.txt`, the uninstall (ARP) entries for both
registry views and HKCU, the Start menu `.lnk` and `HKLM\Software\Zatvor`. The raw JSON is in
`results/run-<id>/`.

## Measurements (runs 44945146447 / 44945485474)

| | Inno Setup 6.7.1 | WiX 5.0.2 MSI |
|---|---|---|
| Installer size, bytes (payload 1,746,432) | 2,705,251 / 2,705,196 (2.58 MiB) | 700,416 / 700,416 (0.67 MiB) |
| Build time per installer, s | 1.0–1.6 | 0.9–1.6 |
| Admin prompts | not measured: the runner is already elevated and shows no UAC. Evidence: the manifest says `asInvoker`, and Inno setup re-launches itself elevated because of `PrivilegesRequired=admin`. That is one prompt, expected | not measured: same reason. Evidence: `ALLUSERS=1` and Word Count 2 (bit 3, "elevation not required", is clear). msiexec elevates the execute sequence, so one prompt is expected |
| Silent install | `setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART` | `msiexec /i zatvor.msi /qn /norestart` |
| Silent uninstall | `unins000.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART` (ARP also has `QuietUninstallString` = `/SILENT`) | `msiexec /x {ProductCode} /qn /norestart` |
| Install v0.0.1, s (exit code) | 1.05 / 2.06 (0) | 2.06 / 2.07 (0) |
| Upgrade v0.0.2 over v0.0.1, s (exit code) | 1.02 / 1.01 (0) | 1.01 / 1.01 (0) |
| Upgrade in place | yes: `VERSION.txt` = 0.0.2, 1 ARP entry (`{AppId}_is1`, DisplayVersion 0.0.2) | yes: `VERSION.txt` = 0.0.2, 1 ARP entry (the new ProductCode, DisplayVersion 0.0.2). The old ProductCode is removed |
| Files in Program Files | `zatvor.exe`, `VERSION.txt`, `unins000.exe`, `unins000.dat` | `zatvor.exe`, `VERSION.txt` |
| ARP EstimatedSize, KB | 6027 | 1697 |
| Uninstall, s until entry and files gone | 1.02 + 0.33 / 1.02 + 0.32 | 1.02 + 0.37 / 1.01 + 0.36 |
| Leftovers after uninstall | none: no dir, no ARP entry, no `.lnk`, no `HKLM\Software\Zatvor` | none: same checks |

Install and uninstall times are the `Start-Process -Wait` wall time. The values cluster at 1.0 and 2.0 s, so
they show the wait's polling granularity rather than installer speed. Read them only as "a few seconds or
less", with no difference between the formats.

An earlier run, 44944677232, failed at the Inno uninstall (exit 1) because the log path was relative. The
Inno uninstaller re-launches itself from `%TEMP%`, and a relative `/LOG=` path does not resolve there. Since
then, `Measure.ps1` passes absolute log paths.

## install.ps1 (Windows PowerShell 5.1 and PowerShell 7)

- Ed25519 is `scripts/install/Ed25519.ps1`: pure PowerShell on `System.Numerics.BigInteger`, with SHA-512 from
  `System.Security.Cryptography`. It follows RFC 8032 §5.1.7 with the cofactored check `[8][S]B = [8]R + [8][k]A`.
  It rejects `S ≥ L` and A or R encodings with `y ≥ p` (§5.1.3 step 1), as well as non-points and `x = 0` with
  the sign bit set. One Shamir double-and-add pass computes `[S]B + [k](−A)`.
- `install.ps1` embeds a verbatim copy of `Ed25519.ps1` between `#region Ed25519` and `#endregion Ed25519`, so
  that it is a single file for the one-line install. CI fails if the copy differs.
- Pester 5.9.0, `scripts/install/tests/Ed25519.Tests.ps1`: 11 tests pass on 5.1.26100 and 7.6.10. They cover
  RFC 8032 §7.1 TEST 1–3, TEST 2 with one message bit flipped, `S + L`, non-canonical A, non-canonical R, a
  non-point key and wrong lengths. The two "control" tests show that the non-canonical cases differ from a
  valid signature only in the encoding. A prototype of the same algorithm also showed that each rejection test
  passes (returns true) when its check is removed.

Ed25519 verify time (TEST 3, 7 calls in one session):

| Run | PS 5.1 first / median / max, ms | PS 7.6 first / median / max, ms |
|---|---|---|
| 44944259000 | 306 / 138 / 306 | 259 / 99 / 259 |
| 44944677232 | 321 / 140 / 321 | 152 / 58 / 152 |
| 44945146447 | 188 / 61 / 188 | 139 / 24 / 139 |
| 44945485474 | 315 / 139 / 315 | 258 / 100 / 258 |

Target < 2 s on 5.1: met. The worst value seen, 321 ms, was a first call.

End to end (`scripts/install/tests/E2E.ps1`): CI made a throwaway Ed25519 key with the runner's OpenSSL, signed a
`SHA256SUMS` that lists both installers, and deleted the private key. It then ran a copy of `install.ps1` with
that public key. Result for each case in both runs (5.1 with Inno and with WiX, 7.6 with Inno): all PASS.

| Case | Want | Result |
|---|---|---|
| `CI` unset | exit 2, nothing installed | pass |
| committed `install.ps1` (placeholder TEST key) | exit 1 | pass |
| one signature bit flipped | exit 1 | pass |
| `SHA256SUMS` changed after signing | exit 1 | pass |
| installer changed (hash mismatch) | exit 1 | pass |
| valid release, `-Silent` | exit 0, installed; then uninstall with no leftovers | pass, 1.5–1.9 s for the whole script including the installer |

## install.sh (`scripts/install/tests/install-sh-test.sh`)

| Runner | `openssl` on PATH | Result |
|---|---|---|
| ubuntu-latest | `/usr/bin/openssl`, OpenSSL 3.5.5 | all 8 cases pass; default openssl verifies |
| macos-latest | `/opt/homebrew/bin/openssl`, OpenSSL 4.1.0 | all 8 cases pass; default openssl verifies |
| macos-latest, `OPENSSL=/usr/bin/openssl` | LibreSSL 3.3.6 | refused, exit 3: "brew install openssl@3, then rerun with OPENSSL=..." |
| both, PATH shim `openssl` without Ed25519 | shim | refused, exit 3, with the OS-specific hint |

The capability probe verifies RFC 8032 TEST 2 before anything else. TEST 1 cannot serve as the probe: its
message is empty, and OpenSSL 3.0 cannot `-rawin`-verify an empty input ("Could not allocate 0 bytes", seen
locally with 3.0.13). The empty-message case does not occur in real use, because a `SHA256SUMS` is never empty.
