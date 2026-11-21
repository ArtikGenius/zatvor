<#
.SYNOPSIS
End-to-end test of install.ps1 on a CI runner: refusals, signature and hash
failures, then a real silent install and uninstall.

.DESCRIPTION
-Release holds SHA256SUMS, SHA256SUMS.sig (signed in CI with a throwaway key
made for this run) and the installer. install.ps1 is copied with its embedded
key replaced by -PublicKeyHex; the committed install.ps1 is also run once to
show its placeholder key does not accept the CI signature.
#>
param(
    [Parameter(Mandatory)][string]$Release,
    [Parameter(Mandatory)][string]$Installer,
    [Parameter(Mandatory)][string]$PublicKeyHex,
    [Parameter(Mandatory)][string]$Out,
    [string]$Shell = 'powershell.exe'
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\..\..\proto\installer\InstallerLib.ps1')
Assert-ZatvorCI

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$Release = (Resolve-Path $Release).Path
$work = Join-Path ([IO.Path]::GetTempPath()) ('zatvor-e2e-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null

# install.ps1 with the per-run test key.
$orig = Join-Path $root 'install.ps1'
$text = [IO.File]::ReadAllText($orig)
$keyLines = [regex]::Matches($text, "(?m)^\`$ReleasePublicKeyHex = '([0-9a-f]{64})'")
if ($keyLines.Count -ne 1) { throw 'key line not found exactly once in install.ps1' }
$patched = Join-Path $work 'install.ps1'
[IO.File]::WriteAllText($patched, $text.Replace($keyLines[0].Value, "`$ReleasePublicKeyHex = '$PublicKeyHex'"))

function New-ReleaseCopy([string]$Name) {
    $d = Join-Path $work $Name
    New-Item -ItemType Directory -Path $d | Out-Null
    Copy-Item (Join-Path $Release '*') $d
    $d
}

function Invoke-InstallPs1([string]$Script, [string]$Source, [hashtable]$Env = @{}) {
    $ErrorActionPreference = 'Continue' # stderr of the child is data here
    $saved = @{}
    foreach ($k in $Env.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k); [Environment]::SetEnvironmentVariable($k, $Env[$k]) }
    try {
        $sw = [Diagnostics.Stopwatch]::StartNew()
        $output = & $Shell -NoProfile -ExecutionPolicy Bypass -File $Script -Source $Source -Installer $Installer -Silent 2>&1 | Out-String
        $code = $LASTEXITCODE
        $sw.Stop()
    } finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }
    [ordered]@{ exitCode = $code; seconds = [math]::Round($sw.Elapsed.TotalSeconds, 2); output = $output.Trim(); installedAfter = (@(Get-ZatvorArp).Count -gt 0) }
}

$cases = [ordered]@{}
$fail = @()
function Check([string]$Name, $Result, [int]$WantCode, [bool]$WantInstalled) {
    $Result.want = "exit $WantCode, installed=$WantInstalled"
    $Result.pass = ($Result.exitCode -eq $WantCode -and $Result.installedAfter -eq $WantInstalled)
    $script:cases[$Name] = $Result
    if (-not $Result.pass) { $script:fail += $Name }
    "{0,-34} exit {1} ({2} s) installed={3} -> {4}" -f $Name, $Result.exitCode, $Result.seconds, $Result.installedAfter, $(if ($Result.pass) { 'PASS' } else { 'FAIL' })
}

try {
    if (@(Get-ZatvorArp).Count -gt 0) { throw 'Zatvor is already installed before the test' }

    Check 'refuses without CI=true' (Invoke-InstallPs1 $patched $Release @{ CI = $null }) 2 $false

    Check 'committed placeholder key' (Invoke-InstallPs1 $orig $Release) 1 $false

    $d = New-ReleaseCopy 'badsig'
    $sigPath = Join-Path $d 'SHA256SUMS.sig'
    $b = [IO.File]::ReadAllBytes($sigPath); $b[10] = $b[10] -bxor 0x01; [IO.File]::WriteAllBytes($sigPath, $b)
    Check 'signature byte flipped' (Invoke-InstallPs1 $patched $d) 1 $false

    $d = New-ReleaseCopy 'badsums'
    Add-Content -LiteralPath (Join-Path $d 'SHA256SUMS') -Value ('0' * 64 + '  extra.exe') -NoNewline
    Check 'SHA256SUMS changed after signing' (Invoke-InstallPs1 $patched $d) 1 $false

    $d = New-ReleaseCopy 'badinstaller'
    $fs = [IO.File]::Open((Join-Path $d $Installer), 'Append'); $fs.WriteByte(0); $fs.Close()
    Check 'installer changed (hash mismatch)' (Invoke-InstallPs1 $patched $d) 1 $false

    Check 'valid release, silent install' (Invoke-InstallPs1 $patched $Release) 0 $true
    $cases['valid release, silent install'].state = Get-ZatvorState
    $cases['valid release, silent install'].uninstall = Invoke-ZatvorUninstall (Join-Path $work 'uninstall.log')
    $cases['valid release, silent install'].leftovers = Get-ZatvorState
} finally {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Out) | Out-Null
    [ordered]@{ installer = $Installer; shell = $Shell; cases = $cases; failed = $fail } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $Out -Encoding UTF8
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
if ($fail.Count -gt 0) { throw "failed: $($fail -join ', ')" }
