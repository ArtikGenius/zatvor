<#
.SYNOPSIS
Installs, upgrades and uninstalls one installer prototype and records the
task 10 measurements as JSON. CI only (it changes the system).

.EXAMPLE
powershell -File proto\installer\Measure.ps1 -Format inno -V1 out\inno\zatvor-setup-0.0.1.exe -V2 out\inno\zatvor-setup-0.0.2.exe -Out out\results\inno.json
#>
param(
    [Parameter(Mandatory)][ValidateSet('inno', 'wix')][string]$Format,
    [Parameter(Mandatory)][string]$V1,
    [Parameter(Mandatory)][string]$V2,
    [Parameter(Mandatory)][string]$Out
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'InstallerLib.ps1')
Assert-ZatvorCI

$V1 = (Resolve-Path $V1).Path
$V2 = (Resolve-Path $V2).Path
# Absolute: the Inno uninstaller re-launches itself from %TEMP%, where a
# relative /LOG= path does not resolve.
$logDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath((Join-Path (Split-Path -Parent $Out) "logs-$Format"))
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

function Get-ElevationEvidence([string]$Path) {
    if ($Format -eq 'inno') {
        $text = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($Path))
        $levels = @([regex]::Matches($text, 'requestedExecutionLevel\s+level="(\w+)"') | ForEach-Object { $_.Groups[1].Value })
        return [ordered]@{ manifestRequestedExecutionLevel = $levels }
    }
    $wi = New-Object -ComObject WindowsInstaller.Installer
    $t = $wi.GetType()
    $si = $t.InvokeMember('SummaryInformation', 'GetProperty', $null, $wi, @($Path, 0))
    $wordCount = $si.GetType().InvokeMember('Property', 'GetProperty', $null, $si, @(15))
    $db = $t.InvokeMember('OpenDatabase', 'InvokeMethod', $null, $wi, @($Path, 0))
    $props = [ordered]@{}
    foreach ($name in 'ALLUSERS', 'MSIINSTALLPERUSER', 'ProductVersion', 'UpgradeCode', 'ProductCode') {
        $view = $db.GetType().InvokeMember('OpenView', 'InvokeMethod', $null, $db, @("SELECT ``Value`` FROM ``Property`` WHERE ``Property``='$name'"))
        $view.GetType().InvokeMember('Execute', 'InvokeMethod', $null, $view, $null) | Out-Null
        $rec = $view.GetType().InvokeMember('Fetch', 'InvokeMethod', $null, $view, $null)
        $props[$name] = $(if ($null -ne $rec) { $rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(1)) } else { $null })
        $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null
    }
    foreach ($o in @($si, $db, $wi)) { [void][Runtime.InteropServices.Marshal]::ReleaseComObject($o) }
    [ordered]@{
        summaryWordCount              = $wordCount
        # Word Count bit 3 (8) set = "elevated privileges are not required".
        elevationNotRequiredBit       = [bool]($wordCount -band 8)
        properties                    = $props
    }
}

$r = [ordered]@{
    format           = $Format
    runner           = [ordered]@{ os = [Environment]::OSVersion.VersionString; ps = "$($PSVersionTable.PSVersion)"; elevated = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) }
    sizeBytes        = [ordered]@{ v1 = (Get-Item $V1).Length; v2 = (Get-Item $V2).Length }
    payloadBytes     = (Get-Item (Join-Path (Split-Path -Parent $V1) '..\src-0.0.1\zatvor.exe') -ErrorAction SilentlyContinue).Length
    elevation        = Get-ElevationEvidence $V1
    silentInstall    = $null
    before           = Get-ZatvorState
}
try {
    if ($r.before.dirExists -or $r.before.arpEntries.Count -gt 0) { throw 'Zatvor is already present before the test' }

    $c = Get-InstallerCommand $V1 (Join-Path $logDir 'install-v1.log')
    $r.silentInstall = ($c.Args | Where-Object { $_ -notlike '/LOG*' -and $_ -notlike '/l*v' -and $_ -notlike '"*' }) -join ' '
    $r.installV1 = Invoke-Timed $c.File $c.Args
    $r.afterV1 = Get-ZatvorState

    $c = Get-InstallerCommand $V2 (Join-Path $logDir 'install-v2-over-v1.log')
    $r.upgradeV2 = Invoke-Timed $c.File $c.Args
    $r.afterV2 = Get-ZatvorState
    $r.upgradeInPlace = [ordered]@{
        versionTxtIs002 = ($r.afterV2.versionTxt -eq '0.0.2')
        arpEntryCount   = $r.afterV2.arpEntries.Count
        arpVersion      = @($r.afterV2.arpEntries | ForEach-Object { $_.DisplayVersion })
    }

    $uLog = Join-Path $logDir 'uninstall.log'
    $r.uninstall = Invoke-ZatvorUninstall $uLog
    if ($r.uninstall.exitCode -ne 0 -and (Test-Path -LiteralPath $uLog)) { $r.uninstall.logTail = @(Get-Content -LiteralPath $uLog -Tail 30) }
    $r.leftovers = Get-ZatvorState
} catch {
    $r.error = "$_"
    throw
} finally {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Out) | Out-Null
    $r | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $Out -Encoding UTF8
    Get-Content -LiteralPath $Out
}
