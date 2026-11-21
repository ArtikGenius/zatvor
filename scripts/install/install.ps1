<#
.SYNOPSIS
Zatvor one-line installer for Windows: verifies the signed SHA256SUMS, then the
installer's hash, then runs the installer elevated (spec section 11, R3.2).

.DESCRIPTION
Stage 0c prototype. There is no network release yet, so -Source names a local
directory that holds SHA256SUMS, SHA256SUMS.sig and the installer; the files
are copied to a private temporary directory first and only the copies are
checked and run. Order: Ed25519 signature of SHA256SUMS with the embedded
release key -> SHA-256 of the installer against SHA256SUMS -> installer.

Runs on Windows PowerShell 5.1 (no Ed25519 there: a compact verifier on
System.Numerics.BigInteger is embedded below) and on PowerShell 7.

In stage 0c it changes the system, so it refuses unless CI=true.

Exit codes: 0 installed; 1 verification failed (nothing was run);
2 refused or bad usage; 3 the installer failed.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File install.ps1 -Source C:\release -Installer zatvor-setup-0.0.1.exe -Silent
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Source,
    [string]$Installer = 'zatvor-setup.exe',
    [switch]$Silent
)

$ErrorActionPreference = 'Stop'

# Release public key: raw 32-byte Ed25519 key, hex.
# TEST KEY -- replaced in stage 1 by Artur's release key. It is a throwaway key
# generated for stage 0c; its private half was discarded and signs nothing.
$ReleasePublicKeyHex = '91bcb4d413b29b1c47ba53949e31fa02636289b7e796b49b79734808395513a1'

#region Ed25519 -- verbatim copy of Ed25519.ps1 (CI checks they are identical)
# Ed25519 signature verification (RFC 8032 section 5.1.7) for Windows
# PowerShell 5.1, which has no Ed25519 of its own. Pure PowerShell on
# System.Numerics.BigInteger; SHA-512 from System.Security.Cryptography.
# Also runs on PowerShell 7. Verification only: no secret data is handled,
# so the code does not need to be constant-time.
#
# Usage: . .\Ed25519.ps1; Test-Ed25519Signature -PublicKey $pk -Message $m -Signature $sig

Add-Type -AssemblyName System.Numerics

# Curve constants. Built per call and passed to the helpers, so the code
# does not depend on which scope the file was dot-sourced into.
function Get-Ed25519Constants {
    $BI = [System.Numerics.BigInteger]
    $p = $BI::Pow(2, 255) - 19
    $d = $BI::Remainder((($p - 121665) * $BI::ModPow(121666, $p - 2, $p)), $p)
    @{
        P     = $p
        # Order of the base point (RFC 8032 section 5.1).
        L     = $BI::Pow(2, 252) + $BI::Parse('27742317777372353535851937790883648493')
        D     = $d
        D2    = $BI::Remainder($d + $d, $p)
        # sqrt(-1) mod p
        SqrtM1 = $BI::ModPow(2, ($p - 1) / 4, $p)
        # Base point B (RFC 8032 section 5.1).
        Bx    = $BI::Parse('15112221349535400772501151409588531511454012693041857206046113283949847762202')
        By    = $BI::Parse('46316835694926478169428394003475163141307993866256225615783033603165251855960')
        Zero  = $BI::Zero
        One   = $BI::One
    }
}

# Little-endian unsigned integer from bytes [$Offset, $Offset + $Count).
function ConvertTo-Ed25519Int([byte[]]$Bytes, [int]$Offset, [int]$Count) {
    $buf = New-Object byte[] ($Count + 1) # extra zero byte keeps the value non-negative
    [Array]::Copy($Bytes, $Offset, $buf, 0, $Count)
    return [System.Numerics.BigInteger]::new($buf)
}

function Get-Ed25519Mod($c, $x) {
    $r = [System.Numerics.BigInteger]::Remainder($x, $c.P)
    if ($r.Sign -lt 0) { $r = $r + $c.P }
    return $r
}

# Point decoding, RFC 8032 section 5.1.3. Returns extended coordinates
# @(X, Y, Z, T), or $null when the encoding is invalid or non-canonical.
function ConvertFrom-Ed25519Point($c, [byte[]]$Bytes, [int]$Offset) {
    $p = $c.P
    $BI = [System.Numerics.BigInteger]
    $buf = New-Object byte[] 33
    [Array]::Copy($Bytes, $Offset, $buf, 0, 32)
    $x0 = ($buf[31] -shr 7) -band 1
    $buf[31] = $buf[31] -band 0x7f
    $y = $BI::new($buf)
    # Step 1: y must be < p, otherwise decoding fails (non-canonical).
    if ($y.CompareTo($p) -ge 0) { return $null }
    # Step 2: x^2 = (y^2 - 1) / (d y^2 + 1).
    $y2 = $BI::Remainder($y * $y, $p)
    $u = Get-Ed25519Mod $c ($y2 - 1)
    $v = $BI::Remainder($c.D * $y2 + 1, $p)
    # Step 3: candidate root x = u v^3 (u v^7)^((p-5)/8).
    $v3 = $BI::Remainder($v * $v * $v, $p)
    $v7 = $BI::Remainder($v3 * $v3 * $v, $p)
    $x = $BI::Remainder($u * $v3 * $BI::ModPow($BI::Remainder($u * $v7, $p), ($p - 5) / 8, $p), $p)
    $vx2 = $BI::Remainder($v * $x * $x, $p)
    if ($vx2.Equals($u)) {
    } elseif ($vx2.Equals((Get-Ed25519Mod $c ($p - $u)))) {
        $x = $BI::Remainder($x * $c.SqrtM1, $p)
    } else {
        return $null # no square root: not a curve point
    }
    # Step 4: sign of x.
    if ($x.IsZero -and $x0 -eq 1) { return $null }
    $xLow = [int]($BI::Remainder($x, 2))
    if ($xLow -ne $x0) { $x = $p - $x }
    return , @($x, $y, $c.One, $BI::Remainder($x * $y, $p))
}

# Point addition in extended coordinates, RFC 8032 section 5.1.4.
function Add-Ed25519Point($c, $P1, $P2) {
    $p = $c.P
    $BI = [System.Numerics.BigInteger]
    $a = $BI::Remainder(($P1[1] - $P1[0] + $p) * ($P2[1] - $P2[0] + $p), $p)
    $b = $BI::Remainder(($P1[1] + $P1[0]) * ($P2[1] + $P2[0]), $p)
    $cc = $BI::Remainder($P1[3] * $c.D2 * $P2[3], $p)
    $dd = $BI::Remainder(($P1[2] + $P1[2]) * $P2[2], $p)
    $e = $b - $a + $p
    $f = $dd - $cc + $p
    $g = $dd + $cc
    $h = $b + $a
    return , @($BI::Remainder($e * $f, $p), $BI::Remainder($g * $h, $p), $BI::Remainder($f * $g, $p), $BI::Remainder($e * $h, $p))
}

# Point doubling, RFC 8032 section 5.1.4.
function Step-Ed25519Double($c, $P1) {
    $p = $c.P
    $BI = [System.Numerics.BigInteger]
    $a = $BI::Remainder($P1[0] * $P1[0], $p)
    $b = $BI::Remainder($P1[1] * $P1[1], $p)
    $cc = $BI::Remainder(($P1[2] + $P1[2]) * $P1[2], $p)
    $h = $a + $b
    $s = $P1[0] + $P1[1]
    $e = Get-Ed25519Mod $c ($h - $BI::Remainder($s * $s, $p))
    $g = $a - $b + $p
    $f = $cc + $g
    return , @($BI::Remainder($e * $f, $p), $BI::Remainder($g * $h, $p), $BI::Remainder($f * $g, $p), $BI::Remainder($e * $h, $p))
}

function Get-Ed25519Negated($c, $P1) {
    $p = $c.P
    return , @((Get-Ed25519Mod $c ($p - $P1[0])), $P1[1], $P1[2], (Get-Ed25519Mod $c ($p - $P1[3])))
}

<#
.SYNOPSIS
Verifies an Ed25519 signature (RFC 8032 section 5.1.7, pure Ed25519, no context).
.OUTPUTS
[bool] $true only when the signature is valid. Any malformed input gives $false.
#>
function Test-Ed25519Signature {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory)][byte[]]$PublicKey,
        [Parameter(Mandatory)][AllowEmptyCollection()][byte[]]$Message,
        [Parameter(Mandatory)][byte[]]$Signature
    )
    if ($PublicKey.Length -ne 32 -or $Signature.Length -ne 64) { return $false }
    $c = Get-Ed25519Constants
    $BI = [System.Numerics.BigInteger]

    # Step 1: decode A and R (canonical encodings only) and S; S must be < L.
    $A = ConvertFrom-Ed25519Point $c $PublicKey 0
    if ($null -eq $A) { return $false }
    $R = ConvertFrom-Ed25519Point $c $Signature 0
    if ($null -eq $R) { return $false }
    $S = ConvertTo-Ed25519Int $Signature 32 32
    if ($S.CompareTo($c.L) -ge 0) { return $false }

    # Step 2: k = SHA-512(R || A || M) mod L.
    $buf = New-Object byte[] (64 + $Message.Length)
    [Array]::Copy($Signature, 0, $buf, 0, 32)
    [Array]::Copy($PublicKey, 0, $buf, 32, 32)
    if ($Message.Length -gt 0) { [Array]::Copy($Message, 0, $buf, 64, $Message.Length) }
    $sha = [System.Security.Cryptography.SHA512]::Create()
    try { $digest = $sha.ComputeHash($buf) } finally { $sha.Dispose() }
    $k = $BI::Remainder((ConvertTo-Ed25519Int $digest 0 64), $c.L)

    # Step 3: check [8][S]B = [8]R + [8][k]A, as [8]([S]B + [k](-A) - R) = 0.
    # [S]B + [k](-A) is computed in one double-and-add pass (Shamir's trick).
    $B = @($c.Bx, $c.By, $c.One, $BI::Remainder($c.Bx * $c.By, $c.P))
    $negA = Get-Ed25519Negated $c $A
    $BnegA = Add-Ed25519Point $c $B $negA
    $sBytes = $S.ToByteArray()
    $kBytes = $k.ToByteArray()
    $Q = @($c.Zero, $c.One, $c.One, $c.Zero)
    for ($i = 252; $i -ge 0; $i--) { # S, k < L < 2^253
        $Q = Step-Ed25519Double $c $Q
        $byte = $i -shr 3
        $sb = if ($byte -lt $sBytes.Length) { ($sBytes[$byte] -shr ($i -band 7)) -band 1 } else { 0 }
        $kb = if ($byte -lt $kBytes.Length) { ($kBytes[$byte] -shr ($i -band 7)) -band 1 } else { 0 }
        if ($sb -eq 1 -and $kb -eq 1) { $Q = Add-Ed25519Point $c $Q $BnegA }
        elseif ($sb -eq 1) { $Q = Add-Ed25519Point $c $Q $B }
        elseif ($kb -eq 1) { $Q = Add-Ed25519Point $c $Q $negA }
    }
    $Q = Add-Ed25519Point $c $Q (Get-Ed25519Negated $c $R)
    for ($i = 0; $i -lt 3; $i++) { $Q = Step-Ed25519Double $c $Q }
    # Identity in extended coordinates: X = 0 and Y = Z.
    return ($Q[0].IsZero -and $Q[1].Equals($Q[2]))
}
#endregion Ed25519

function Write-InstallError([string]$Text) { [Console]::Error.WriteLine("install.ps1: $Text") }

function ConvertFrom-InstallHex([string]$Hex) {
    $bytes = New-Object byte[] ($Hex.Length / 2)
    for ($i = 0; $i -lt $bytes.Length; $i++) { $bytes[$i] = [Convert]::ToByte($Hex.Substring(2 * $i, 2), 16) }
    return , $bytes
}

if ($env:CI -ne 'true') {
    Write-InstallError 'stage 0c prototype: runs only on CI runners (CI=true); it would change this system'
    exit 2
}
if ($Installer -notmatch '^[A-Za-z0-9._-]+$') { Write-InstallError "bad installer name: $Installer"; exit 2 }

$work = Join-Path ([IO.Path]::GetTempPath()) ('zatvor-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    # "Download": stage 0c copies from a local directory.
    foreach ($name in @('SHA256SUMS', 'SHA256SUMS.sig', $Installer)) {
        $from = Join-Path $Source $name
        if (-not (Test-Path -LiteralPath $from -PathType Leaf)) { Write-InstallError "missing $name in $Source"; exit 1 }
        Copy-Item -LiteralPath $from -Destination (Join-Path $work $name)
    }

    # 1. Signature of SHA256SUMS.
    $sums = [IO.File]::ReadAllBytes((Join-Path $work 'SHA256SUMS'))
    $sig = [IO.File]::ReadAllBytes((Join-Path $work 'SHA256SUMS.sig'))
    $key = ConvertFrom-InstallHex $ReleasePublicKeyHex
    if (-not (Test-Ed25519Signature -PublicKey $key -Message $sums -Signature $sig)) {
        Write-InstallError 'SHA256SUMS signature is NOT valid for the Zatvor release key; nothing was installed'
        exit 1
    }
    Write-Host 'SHA256SUMS signature: valid'

    # 2. Hash of the installer, from the signed list.
    $want = @()
    foreach ($line in ([Text.Encoding]::UTF8.GetString($sums) -split "`n")) {
        if ($line.TrimEnd("`r") -match '^([0-9a-fA-F]{64}) [ *](.+)$' -and $Matches[2] -ceq $Installer) { $want += $Matches[1].ToLowerInvariant() }
    }
    if ($want.Count -ne 1) { Write-InstallError "SHA256SUMS lists $Installer $($want.Count) times (want 1); nothing was installed"; exit 1 }
    $path = Join-Path $work $Installer
    $got = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($got -ne $want[0]) { Write-InstallError "$Installer SHA-256 $got does not match SHA256SUMS ($($want[0])); nothing was installed"; exit 1 }
    Write-Host "$Installer SHA-256: matches"

    # 3. Run the installer elevated (one UAC prompt when not elevated yet).
    if ($Installer -like '*.msi') {
        $file = Join-Path $env:SystemRoot 'System32\msiexec.exe'
        $argList = @('/i', "`"$path`"")
        if ($Silent) { $argList += @('/qn', '/norestart') }
        $okCodes = @(0, 3010)
    } else {
        $file = $path
        $argList = @()
        if ($Silent) { $argList += @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') }
        $okCodes = @(0)
    }
    $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    $start = @{ FilePath = $file; Wait = $true; PassThru = $true }
    if ($argList.Count -gt 0) { $start.ArgumentList = $argList }
    if (-not $admin) { $start.Verb = 'RunAs' }
    $proc = Start-Process @start
    if ($okCodes -notcontains $proc.ExitCode) { Write-InstallError "installer exited with $($proc.ExitCode)"; exit 3 }
    Write-Host "installed ($Installer, exit $($proc.ExitCode))"
    exit 0
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
