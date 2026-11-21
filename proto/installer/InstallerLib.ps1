# Helpers shared by Measure.ps1 and scripts/install/tests/E2E.ps1 (stage 0c,
# task 10). They install and remove software, so callers check CI=true first.

$script:ZatvorDir = Join-Path $env:ProgramFiles 'Zatvor'
$script:ZatvorLnk = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\Zatvor.lnk'
$script:ZatvorHklm = 'HKLM:\SOFTWARE\Zatvor'

function Assert-ZatvorCI {
    if ($env:CI -ne 'true') {
        [Console]::Error.WriteLine('runs only on CI runners (CI=true): it installs and removes software')
        exit 2
    }
}

# "Apps & features" entries for Zatvor (machine-wide, both registry views).
function Get-ZatvorArp {
    $roots = @(
        'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall'
    )
    foreach ($root in $roots) {
        foreach ($k in (Get-ChildItem -LiteralPath $root -ErrorAction SilentlyContinue)) {
            $p = Get-ItemProperty -LiteralPath $k.PSPath
            if ("$($p.DisplayName)" -like 'Zatvor*') {
                [pscustomobject][ordered]@{
                    Key                  = $k.Name
                    DisplayName          = $p.DisplayName
                    DisplayVersion       = $p.DisplayVersion
                    InstallLocation      = $p.InstallLocation
                    UninstallString      = $p.UninstallString
                    QuietUninstallString = $p.QuietUninstallString
                    EstimatedSizeKB      = $p.EstimatedSize
                }
            }
        }
    }
}

function Get-ZatvorState {
    $exe = Join-Path $script:ZatvorDir 'zatvor.exe'
    $ver = Join-Path $script:ZatvorDir 'VERSION.txt'
    $files = @()
    if (Test-Path -LiteralPath $script:ZatvorDir) {
        $files = @(Get-ChildItem -LiteralPath $script:ZatvorDir -Recurse -Force | ForEach-Object { $_.FullName.Substring($script:ZatvorDir.Length + 1) })
    }
    $exeOut = $null
    if (Test-Path -LiteralPath $exe) { $exeOut = (& $exe --version 2>&1 | Out-String).Trim() }
    [ordered]@{
        dirExists       = (Test-Path -LiteralPath $script:ZatvorDir)
        files           = $files
        versionTxt      = $(if (Test-Path -LiteralPath $ver) { (Get-Content -LiteralPath $ver -Raw).Trim() } else { $null })
        exeVersionOut   = $exeOut
        arpEntries      = @(Get-ZatvorArp)
        startMenuLnk    = (Test-Path -LiteralPath $script:ZatvorLnk)
        hklmSoftwareKey = (Test-Path -LiteralPath $script:ZatvorHklm)
    }
}

function Get-InstallerCommand([string]$Path, [string]$Log) {
    if ($Path -like '*.msi') {
        @{ File = (Join-Path $env:SystemRoot 'System32\msiexec.exe'); Args = @('/i', "`"$Path`"", '/qn', '/norestart', '/l*v', "`"$Log`"") }
    } else {
        @{ File = $Path; Args = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/LOG=`"$Log`"") }
    }
}

function Invoke-Timed([string]$File, [string[]]$ArgList) {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    $p = Start-Process -FilePath $File -ArgumentList $ArgList -Wait -PassThru
    $sw.Stop()
    [ordered]@{ exitCode = $p.ExitCode; seconds = [math]::Round($sw.Elapsed.TotalSeconds, 2); command = "$File $($ArgList -join ' ')" }
}

# Waits until the uninstall entry and the install dir are gone (the Inno
# uninstaller re-launches itself from %TEMP%, so the first process exits early).
function Wait-ZatvorGone([int]$TimeoutSeconds = 120) {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    while ($sw.Elapsed.TotalSeconds -lt $TimeoutSeconds) {
        if (@(Get-ZatvorArp).Count -eq 0 -and -not (Test-Path -LiteralPath (Join-Path $script:ZatvorDir 'zatvor.exe'))) { break }
        Start-Sleep -Milliseconds 250
    }
    # Give a self-deleting uninstaller a moment to finish removing the dir.
    $settle = [Diagnostics.Stopwatch]::StartNew()
    while ((Test-Path -LiteralPath $script:ZatvorDir) -and $settle.Elapsed.TotalSeconds -lt 15) { Start-Sleep -Milliseconds 250 }
    [math]::Round($sw.Elapsed.TotalSeconds, 2)
}

# Silent uninstall from the ARP entry. Returns the command result.
function Invoke-ZatvorUninstall([string]$Log) {
    $arp = @(Get-ZatvorArp)
    if ($arp.Count -ne 1) { throw "want exactly one Zatvor ARP entry, found $($arp.Count)" }
    $e = $arp[0]
    $code = Split-Path -Leaf $e.Key
    if ($code -match '^\{[0-9A-Fa-f-]{36}\}$' -and "$($e.UninstallString)" -match 'msiexec') {
        $r = Invoke-Timed (Join-Path $env:SystemRoot 'System32\msiexec.exe') @('/x', $code, '/qn', '/norestart', '/l*v', "`"$Log`"")
    } else {
        $exe = "$($e.UninstallString)".Trim().Trim('"')
        $r = Invoke-Timed $exe @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/LOG=`"$Log`"")
    }
    $r.secondsUntilGone = Wait-ZatvorGone
    $r
}
