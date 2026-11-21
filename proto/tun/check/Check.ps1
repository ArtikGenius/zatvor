<#
.SYNOPSIS
  TUN front prototype check (stage 0c, task 8): measures variant A (Xray tun
  inbound) or B (sing-tun -> Xray SOCKS) and writes results\tun-<variant>.json.

.DESCRIPTION
  Runs ONLY on a disposable GitHub Actions Windows runner: it installs a TUN
  adapter, system routes, DNS settings and WFP filters, and kills the TUN
  owner on purpose. It refuses (exit 2) unless CI=true and ZATVOR_ALLOW_LIVE=1.

  Expects (built by .github/workflows/proto-tun.yml):
    <Work>\bin\a.exe, b.exe, bench.exe, pcapcount.exe   (go build ./proto/tun/...)
    <Work>\xray\xray.exe                                (go run ./tools/xrayfetch)

  Topology, all on the runner:
    bench serve   127.0.0.1:18080 (HTTP "zatvor-tunnel"), 127.0.0.1:18081 (zero stream)
    upstream Xray 127.0.0.1:10800 SOCKS, freedom bound to the physical adapter,
                  probe addresses redirected to bench, DNS answered by its dns
                  outbound over DoH (never port 53)  [proto/tun/upstream/config.json]
    A: client Xray, tun inbound -> socks 127.0.0.1:10800
    B: b.exe (sing-tun) -> client Xray socks 127.0.0.1:10808 -> 127.0.0.1:10800

  Leak method: pktmon captures the physical adapter only (components picked
  by MAC address) while a scripted workload runs without curl.exe. A DNS leak
  is any TCP/UDP packet with port 53 or 853; a bypass is any packet to or from
  a probe address (198.18.0.10, 198.18.0.20, 2001:db8::10) or 1.1.1.1:80. The
  same capture without the tunnel is the control that shows the method sees
  leaks. Limits are in proto/tun/RESULTS.md.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('A', 'B')][string]$Variant,
    [string]$Work = 'work',
    [string]$ResultsDir = 'results'
)

if ($env:CI -ne 'true' -or $env:ZATVOR_ALLOW_LIVE -ne '1') {
    [Console]::Error.WriteLine('Check.ps1 runs only on CI runners (needs CI=true and ZATVOR_ALLOW_LIVE=1)')
    exit 2
}

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# Pinned Wintun (variant A needs wintun.dll next to xray.exe). The value was
# not computable in the authoring sandbox (wintun.net blocked there); it is
# the published SHA-256 of wintun-0.14.1.zip, and the CI log prints the hash
# of the downloaded file, which must equal it or the run stops.
$WintunUrl = 'https://www.wintun.net/builds/wintun-0.14.1.zip'
$WintunSha256 = '07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51'

$Work = (New-Item -ItemType Directory -Force $Work).FullName
$ResultsDir = (New-Item -ItemType Directory -Force $ResultsDir).FullName
$Logs = (New-Item -ItemType Directory -Force (Join-Path $Work 'logs')).FullName
$Bin = Join-Path $Work 'bin'
$Here = $PSScriptRoot
$TunName = if ($Variant -eq 'A') { 'zatvor-a' } else { 'zatvor-b' }
$ProbeHttp4 = '198.18.0.10'
$ProbeHttp6 = '2001:db8::10'
$ProbeBench = '198.18.0.20:5201'
$Procs = [ordered]@{}
$R = [ordered]@{
    variant          = $Variant
    dnsLeakV4        = 'not measured'
    dnsLeakV6        = 'not measured'
    bypassPackets    = 'not measured'
    killRecovery     = 'not measured'
    adapterLeftovers = 'not measured'
    throughputMbps   = 'not measured'
    cpuIdlePct       = 'not measured'
    perAppRule       = 'not measured'
    sleepWake        = 'not measured in 0c, moved to stage 3 Hyper-V VM check (a CI runner cannot sleep)'
    details          = [ordered]@{}
}
$D = $R.details

function Log([string]$m) { Write-Host ("[{0:HH:mm:ss.fff}] {1}" -f (Get-Date), $m) }
function Section([string]$m) { Write-Host "::group::$m"; Log $m }
function EndSection { Write-Host '::endgroup::' }
function Save-Results {
    $f = Join-Path $ResultsDir "tun-$Variant.json"
    $R | ConvertTo-Json -Depth 8 | Set-Content -Encoding utf8 $f
    Log "results written to $f"
}

function Start-Logged([string]$Name, [string]$Exe, [string[]]$ArgList) {
    Log "start $Name : $Exe $($ArgList -join ' ')"
    $p = Start-Process -FilePath $Exe -ArgumentList $ArgList -PassThru -NoNewWindow `
        -RedirectStandardOutput (Join-Path $Logs "$Name.out.log") -RedirectStandardError (Join-Path $Logs "$Name.err.log")
    $Procs[$Name] = $p
    return $p
}

function Show-Log([string]$Name, [int]$Tail = 40) {
    foreach ($s in 'out', 'err') {
        $f = Join-Path $Logs "$Name.$s.log"
        if (Test-Path $f) { Write-Host "--- $Name.$s.log (tail $Tail)"; Get-Content $f -Tail $Tail | Write-Host }
    }
}

# Raw HTTP/1.0 GET over TcpClient from this PowerShell process (not curl.exe,
# which the per-app rule sends direct).
function Get-Http([string]$Ip, [int]$Port = 80, [int]$TimeoutMs = 8000) {
    try {
        $addr = [Net.IPAddress]::Parse($Ip)
        $c = New-Object Net.Sockets.TcpClient($addr.AddressFamily)
        $t = $c.ConnectAsync($addr, $Port)
        if (-not $t.Wait($TimeoutMs)) { $c.Close(); return 'ERR: connect timeout' }
        $s = $c.GetStream(); $s.ReadTimeout = $TimeoutMs
        $h = if ($Ip.Contains(':')) { "[$Ip]" } else { $Ip }
        $req = [Text.Encoding]::ASCII.GetBytes("GET / HTTP/1.0`r`nHost: $h`r`nUser-Agent: zatvor-check`r`n`r`n")
        $s.Write($req, 0, $req.Length)
        $ms = New-Object IO.MemoryStream; $buf = New-Object byte[] 4096
        while (($n = $s.Read($buf, 0, 4096)) -gt 0) { $ms.Write($buf, 0, $n); if ($ms.Length -gt 65536) { break } }
        $c.Close()
        $body = [Text.Encoding]::ASCII.GetString($ms.ToArray())
        if ($body -eq '') { return 'ERR: empty response' }
        return $body
    } catch { return "ERR: $($_.Exception.InnerException.Message) $($_.Exception.Message)" }
}

function Test-Tcp([string]$Ip, [int]$Port, [int]$TimeoutMs = 1000) {
    try {
        $addr = [Net.IPAddress]::Parse($Ip)
        $c = New-Object Net.Sockets.TcpClient($addr.AddressFamily)
        $ok = $c.ConnectAsync($addr, $Port).Wait($TimeoutMs) -and $c.Connected
        $c.Close(); return $ok
    } catch { return $false }
}

function Resolve-Quiet([string]$Name, [string]$Type = 'A', [string]$Server = '') {
    try {
        $a = @{ Name = $Name; Type = $Type; DnsOnly = $true; QuickTimeout = $true; ErrorAction = 'Stop' }
        if ($Server) { $a.Server = $Server }
        $r = Resolve-DnsName @a
        $ips = @($r | Where-Object { $_.IPAddress } | ForEach-Object { $_.IPAddress })
        return "OK: " + ($ips -join ',')
    } catch { return "ERR: $($_.Exception.Message)" }
}

# True if Windows' resolver answers at all (an NXDOMAIN answer counts).
function Test-Dns {
    try { Clear-DnsClientCache } catch {}
    $r = Resolve-Quiet ("k" + [guid]::NewGuid().ToString('n').Substring(0, 10) + ".example.com") 'A'
    return ($r -like 'OK:*' -or $r -like '*does not exist*' -or $r -like '*DNS name does not exist*')
}

function Invoke-Nslookup([string[]]$NsArgs) {
    $o = & nslookup.exe -timeout=2 -retry=1 @NsArgs 2>&1 | Out-String
    return $o.Trim()
}

function Rand([string]$p) { $p + [guid]::NewGuid().ToString('n').Substring(0, 12) }

# The scripted workload of a leak capture. It never uses curl.exe.
function Invoke-Workload([int]$HttpTimeoutMs) {
    $w = [ordered]@{}
    try { Clear-DnsClientCache } catch {}
    $w.resolveA = Resolve-Quiet (Rand 'zl4-') 'A'
    $w.resolveAAAA = Resolve-Quiet (Rand 'zl6-') 'AAAA'
    $w.resolveRealA = Resolve-Quiet 'www.wikipedia.org' 'A'
    $w.resolveRealAAAA = Resolve-Quiet 'www.wikipedia.org' 'AAAA'
    $w.resolveProbe = Resolve-Quiet 'zatvor-probe.test' 'A'
    $w.nslookupDefault = Invoke-Nslookup @((Rand 'zn-') + '.example.org')
    $w.nslookupAAAA = Invoke-Nslookup @('-type=AAAA', 'www.wikipedia.org')
    $w.nslookupPublic = Invoke-Nslookup @('zatvor-probe.test', '1.1.1.1')
    $w.nslookupPublic6 = Invoke-Nslookup @('zatvor-probe.test', '2606:4700:4700::1111')
    $w.httpProbe4 = Get-Http $ProbeHttp4 80 $HttpTimeoutMs
    $w.httpProbe6 = Get-Http $ProbeHttp6 80 $HttpTimeoutMs
    $w.http1111 = Get-Http '1.1.1.1' 80 $HttpTimeoutMs
    $w.tcpBench = Test-Tcp '198.18.0.20' 5201 $HttpTimeoutMs
    Start-Sleep -Seconds 10 # background traffic of the runner itself
    foreach ($k in @($w.Keys)) { if ($w[$k] -is [string] -and $w[$k].Length -gt 400) { $w[$k] = $w[$k].Substring(0, 400) } }
    return $w
}

# pktmon components of the physical adapter(s), matched by MAC address.
function Get-PhysComponents {
    $macs = @(Get-NetAdapter -Physical | ForEach-Object { $_.MacAddress.ToUpper() })
    $txt = (& pktmon list 2>&1 | Out-String)
    Set-Content -Path (Join-Path $Logs "pktmon-list-$((Get-Date).ToString('HHmmss')).txt") -Value $txt
    $ids = @()
    foreach ($line in $txt -split "`r?`n") {
        if ($line -match '^\s*(\d+)\s+([0-9A-Fa-f]{2}(?:-[0-9A-Fa-f]{2}){5})\s+(.*)$') {
            if ($macs -contains $Matches[2].ToUpper() -and $Matches[3] -notmatch 'Wintun|sing-tun|zatvor') { $ids += $Matches[1] }
        }
    }
    return [ordered]@{ macs = $macs; ids = $ids }
}

function Invoke-Capture([string]$Tag, [int]$HttpTimeoutMs) {
    $comp = Get-PhysComponents
    Log "pktmon components for physical MACs $($comp.macs -join ','): $($comp.ids -join ',')"
    $etl = Join-Path $Logs "$Tag.etl"
    $pcap = Join-Path $Logs "$Tag.pcapng"
    & pktmon stop 2>&1 | Out-Null
    & pktmon filter remove 2>&1 | Out-Null
    $sel = if ($comp.ids.Count -gt 0) { $comp.ids } else { @('nics') }
    $out = & pktmon start --capture --comp @sel --pkt-size 0 --file-name $etl 2>&1 | Out-String
    Log "pktmon start: $($out.Trim())"
    $w = Invoke-Workload $HttpTimeoutMs
    $out = & pktmon stop 2>&1 | Out-String
    Log "pktmon stop: $($out.Trim())"
    $out = & pktmon etl2pcap $etl --out $pcap 2>&1 | Out-String
    Log "pktmon etl2pcap: $($out.Trim())"
    $json = & (Join-Path $Bin 'pcapcount.exe') $pcap 2>&1 | Out-String
    Log "pcapcount $Tag : $json"
    return [ordered]@{
        components = if ($comp.ids.Count -gt 0) { "physical by MAC: $($comp.ids -join ',')" } else { 'FALLBACK all NICs (TUN included)' }
        counts     = ($json | ConvertFrom-Json)
        workload   = $w
    }
}

function Get-Snapshot {
    $s = [ordered]@{}
    $s.adapters = @(Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Name)|$($_.InterfaceDescription)|$($_.Status)" } | Sort-Object)
    $s.pnp = @(Get-PnpDevice -Class Net -ErrorAction SilentlyContinue | ForEach-Object { "$($_.FriendlyName)|$($_.InstanceId)|$($_.Status)" } | Sort-Object)
    $s.routes = @(Get-NetRoute -ErrorAction SilentlyContinue | ForEach-Object { "$($_.InterfaceAlias)|$($_.DestinationPrefix)|$($_.NextHop)" } | Sort-Object -Unique)
    $s.dns = @(Get-DnsClientServerAddress -ErrorAction SilentlyContinue | Where-Object { $_.ServerAddresses } | ForEach-Object { "$($_.InterfaceAlias)|$($_.AddressFamily)|$($_.ServerAddresses -join ',')" } | Sort-Object)
    return $s
}

function Compare-Sets($Before, $After) {
    return [ordered]@{
        added   = @($After | Where-Object { $Before -notcontains $_ })
        removed = @($Before | Where-Object { $After -notcontains $_ })
    }
}

function Get-WfpFilterCount {
    $f = Join-Path $Logs "wfp-$((Get-Date).ToString('HHmmss')).xml"
    & netsh wfp show filters file=$f 2>&1 | Out-Null
    if (-not (Test-Path $f)) { return -1 }
    return @(Select-String -Path $f -Pattern '<name>[^<]*(Xray|xray|sing-tun)[^<]*</name>').Count
}

function Get-TunOwner { if ($Variant -eq 'A') { $Procs['client-xray'] } else { $Procs['b'] } }

try {
    # ---------------------------------------------------------------- preflight
    Section 'preflight'
    $D.os = (Get-CimInstance Win32_OperatingSystem | ForEach-Object { "$($_.Caption) $($_.Version)" })
    $D.cores = [Environment]::ProcessorCount
    $D.runner = $env:RUNNER_NAME
    $def = Get-NetRoute -DestinationPrefix '0.0.0.0/0' | Sort-Object { $_.RouteMetric + $_.InterfaceMetric } | Select-Object -First 1
    $phys = Get-NetAdapter -InterfaceIndex $def.ifIndex
    $D.physicalAdapter = "$($phys.Name) | $($phys.InterfaceDescription) | $($phys.MacAddress)"
    $D.physicalAdapters = @(Get-NetAdapter -Physical | ForEach-Object { "$($_.Name) | $($_.InterfaceDescription) | $($_.MacAddress) | $($_.Status)" })
    $D.ipv6DefaultRoute = [bool](Get-NetRoute -DestinationPrefix '::/0' -ErrorAction SilentlyContinue)
    $D.ipv6Internet = Test-Tcp '2606:4700:4700::1111' 443 3000
    Log "physical: $($D.physicalAdapter); ipv6 default route: $($D.ipv6DefaultRoute); ipv6 internet: $($D.ipv6Internet)"
    Get-NetIPConfiguration | Out-String | Write-Host
    route.exe print | Out-String | Write-Host
    $base = Get-Snapshot
    $D.baselineDns = $base.dns
    EndSection

    Section 'install binaries'
    $xray = Join-Path $Work 'xray\xray.exe'
    foreach ($dir in 'client', 'upstream') {
        New-Item -ItemType Directory -Force (Join-Path $Work $dir) | Out-Null
        Copy-Item $xray (Join-Path $Work "$dir\xray.exe") -Force
    }
    if ($Variant -eq 'A') {
        $zip = Join-Path $Work 'wintun-0.14.1.zip'
        Invoke-WebRequest -Uri $WintunUrl -OutFile $zip -UseBasicParsing
        $h = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
        Log "wintun-0.14.1.zip sha256 $h (pinned $WintunSha256)"
        $D.wintunSha256 = $h
        if ($h -ne $WintunSha256) { throw "wintun zip hash mismatch: got $h, pinned $WintunSha256" }
        Expand-Archive $zip -DestinationPath (Join-Path $Work 'wintun') -Force
        Copy-Item (Join-Path $Work 'wintun\wintun\bin\amd64\wintun.dll') (Join-Path $Work 'client\wintun.dll') -Force
    }
    $logFwd = $Logs -replace '\\', '/'
    (Get-Content -Raw (Join-Path $Here '..\upstream\config.json')).Replace('__PHYS_IF__', $phys.Name).Replace('__LOG_DIR__', $logFwd) |
        Set-Content -Encoding utf8 (Join-Path $Work 'upstream\config.json')
    EndSection

    Section 'start bench and upstream'
    Start-Logged 'bench' (Join-Path $Bin 'bench.exe') @('serve') | Out-Null
    & (Join-Path $Work 'upstream\xray.exe') run -test -c (Join-Path $Work 'upstream\config.json') 2>&1 | Write-Host
    if ($LASTEXITCODE -ne 0) { throw 'upstream config test failed' }
    Start-Logged 'upstream-xray' (Join-Path $Work 'upstream\xray.exe') @('run', '-c', (Join-Path $Work 'upstream\config.json')) | Out-Null
    Start-Sleep -Seconds 3
    $D.upstreamProbe = (& curl.exe -s -m 10 --socks5-hostname 127.0.0.1:10800 "http://$ProbeHttp4/" 2>&1 | Out-String).Trim()
    $D.upstreamInternet = (& curl.exe -s -m 15 -o NUL -w '%{http_code}' --socks5-hostname 127.0.0.1:10800 'https://www.cloudflare.com/cdn-cgi/trace' 2>&1 | Out-String).Trim()
    Log "upstream probe: $($D.upstreamProbe); upstream internet http code: $($D.upstreamInternet)"
    # Real image path of curl.exe as the OS reports it (the per-app rule key).
    $cp = Start-Process curl.exe -ArgumentList @('-s', '-m', '8', '-o', 'NUL', 'telnet://127.0.0.1:18081') -PassThru -NoNewWindow
    Start-Sleep -Milliseconds 800
    $CurlPath = (Get-Process -Id $cp.Id).Path
    Stop-Process -Id $cp.Id -Force -ErrorAction SilentlyContinue
    $D.curlPath = $CurlPath
    Log "curl.exe image path: $CurlPath"
    $lb = & (Join-Path $Bin 'bench.exe') download -addr 127.0.0.1:18081 -seconds 5 | ConvertFrom-Json
    $D.loopbackBaselineMbps = [math]::Round($lb.mbps, 1)
    Log "loopback baseline (no tunnel, no Xray): $($D.loopbackBaselineMbps) Mbit/s"
    EndSection

    Section 'control capture (no tunnel)'
    $D.control = Invoke-Capture 'control' 3000
    EndSection

    # ---------------------------------------------------------------- TUN up
    Section "start variant $Variant"
    $t0 = Get-Date
    if ($Variant -eq 'A') {
        $cfg = Join-Path $Work 'client\config.json'
        & (Join-Path $Bin 'a.exe') -out $cfg -name $TunName -direct-process $CurlPath -log-dir $logFwd
        Get-Content $cfg | Write-Host
        & (Join-Path $Work 'client\xray.exe') run -test -c $cfg 2>&1 | Write-Host
        if ($LASTEXITCODE -ne 0) { throw 'client A config test failed' }
        Start-Logged 'client-xray' (Join-Path $Work 'client\xray.exe') @('run', '-c', $cfg) | Out-Null
    } else {
        $cfg = Join-Path $Work 'client\config.json'
        (Get-Content -Raw (Join-Path $Here '..\b\client-xray.json')).Replace('__LOG_DIR__', $logFwd) | Set-Content -Encoding utf8 $cfg
        & (Join-Path $Work 'client\xray.exe') run -test -c $cfg 2>&1 | Write-Host
        if ($LASTEXITCODE -ne 0) { throw 'client B xray config test failed' }
        Start-Logged 'client-xray' (Join-Path $Work 'client\xray.exe') @('run', '-c', $cfg) | Out-Null
        Start-Sleep -Seconds 2
        Start-Logged 'b' (Join-Path $Bin 'b.exe') @('-name', $TunName, '-socks', '127.0.0.1:10808', '-direct-process', $CurlPath) | Out-Null
    }
    $up = $false
    for ($i = 0; $i -lt 120; $i++) {
        $owner = Get-TunOwner
        if ($owner.HasExited) { Log "TUN owner exited with code $($owner.ExitCode)"; break }
        $ad = Get-NetAdapter -Name $TunName -ErrorAction SilentlyContinue
        $rt = @(Get-NetRoute -InterfaceAlias $TunName -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.DestinationPrefix -match '^(0\.0\.0\.0/[01]|128\.0\.0\.0/1)$' })
        if ($ad -and $ad.Status -eq 'Up' -and $rt.Count -gt 0) { $up = $true; break }
        Start-Sleep -Milliseconds 250
    }
    $D.tunUp = $up
    $D.tunUpSeconds = [math]::Round(((Get-Date) - $t0).TotalSeconds, 2)
    Show-Log 'client-xray'; if ($Variant -eq 'B') { Show-Log 'b' }
    if (-not $up) {
        Get-NetAdapter -IncludeHidden | Format-Table -AutoSize | Out-String | Write-Host
        throw "TUN $TunName did not come up"
    }
    Start-Sleep -Seconds 3
    Log "TUN $TunName up after $($D.tunUpSeconds) s"
    Get-NetIPConfiguration -InterfaceAlias $TunName | Out-String | Write-Host
    route.exe print | Out-String | Write-Host
    Get-DnsClientServerAddress | Format-Table -AutoSize | Out-String | Write-Host
    $D.tunSnapshot = Get-Snapshot
    EndSection

    Section 'tunnel functional check'
    $f = [ordered]@{}
    $f.http4 = Get-Http $ProbeHttp4
    $f.http6 = Get-Http $ProbeHttp6
    $f.dnsProbe = Resolve-Quiet 'zatvor-probe.test' 'A'
    $f.realHttps = (& powershell.exe -NoProfile -Command "try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 15 https://www.cloudflare.com/cdn-cgi/trace).StatusCode } catch { 'ERR ' + `$_.Exception.Message }" 2>&1 | Out-String).Trim()
    $D.functional = $f
    $D.tunnelWorks = ($f.http4 -like '*zatvor-tunnel*')
    $f | ConvertTo-Json | Write-Host
    EndSection

    # ---------------------------------------------------------------- leaks
    Section 'leak capture (tunnel up)'
    $cap = Invoke-Capture 'tunnel' 8000
    $D.tunnelCapture = $cap
    $R.dnsLeakV4 = [int]$cap.counts.dnsV4
    $R.dnsLeakV6 = [int]$cap.counts.dnsV6
    $R.bypassPackets = [int]$cap.counts.probe
    EndSection

    # ---------------------------------------------------------------- per-app
    Section 'per-app rule'
    $p = [ordered]@{}
    $curlOut = (& curl.exe -s -i -m 10 'http://1.1.1.1/' 2>&1 | Out-String)
    $p.curl = if ($curlOut.Length -gt 300) { $curlOut.Substring(0, 300) } else { $curlOut }
    $p.curlDirect = ($curlOut -match '(?im)^server:\s*cloudflare') -and ($curlOut -notmatch 'zatvor-tunnel')
    $p.powershell = Get-Http '1.1.1.1'
    $p.powershellTunnel = ($p.powershell -like '*zatvor-tunnel*')
    $p.nslookup = Invoke-Nslookup @('zatvor-probe.test', '1.1.1.1')
    $p.nslookupTunnel = ($p.nslookup -match '198\.18\.0\.10')
    $R.perAppRule = [bool]($p.curlDirect -and $p.powershellTunnel -and $p.nslookupTunnel)
    $D.perApp = $p
    $p | ConvertTo-Json | Write-Host
    EndSection

    # ---------------------------------------------------------------- throughput
    Section 'throughput (30 s through the tunnel)'
    $tp = & (Join-Path $Bin 'bench.exe') download -addr $ProbeBench -seconds 30 | ConvertFrom-Json
    $D.throughput = $tp
    Log "throughput: $($tp | ConvertTo-Json -Compress)"
    if ($tp.error) { $R.throughputMbps = "not measured: $($tp.error)" } else { $R.throughputMbps = [math]::Round($tp.mbps, 1) }
    EndSection

    # ---------------------------------------------------------------- cpu idle
    Section 'cpu idle (60 s)'
    Start-Sleep -Seconds 5
    $names = if ($Variant -eq 'A') { @('client-xray') } else { @('b', 'client-xray') }
    $before = @{}; foreach ($n in $names) { $before[$n] = (Get-Process -Id $Procs[$n].Id).TotalProcessorTime.TotalSeconds }
    $w0 = Get-Date
    Start-Sleep -Seconds 60
    $wall = ((Get-Date) - $w0).TotalSeconds
    $cpu = [ordered]@{}; $sum = 0.0
    foreach ($n in $names) {
        $s = (Get-Process -Id $Procs[$n].Id).TotalProcessorTime.TotalSeconds - $before[$n]
        $cpu[$n] = [math]::Round($s, 3); $sum += $s
    }
    $cpu.wallSeconds = [math]::Round($wall, 1)
    $cpu.pctOfOneCore = [math]::Round($sum / $wall * 100, 3)
    $D.cpu = $cpu
    $R.cpuIdlePct = [math]::Round($sum / $wall / [Environment]::ProcessorCount * 100, 3)
    Log "cpu idle: $($cpu | ConvertTo-Json -Compress); pct of all cores $($R.cpuIdlePct)"
    EndSection

    # ---------------------------------------------------------------- kill
    Section 'kill -9 recovery'
    $D.wfpFiltersBeforeKill = Get-WfpFilterCount
    $owner = Get-TunOwner
    $tk = Get-Date
    Stop-Process -Id $owner.Id -Force
    $k = [ordered]@{ adapterGoneS = $null; routesCleanS = $null; internetS = $null }
    while (((Get-Date) - $tk).TotalSeconds -lt 30) {
        $el = [math]::Round(((Get-Date) - $tk).TotalSeconds, 2)
        if ($null -eq $k.adapterGoneS -and -not (Get-NetAdapter -Name $TunName -ErrorAction SilentlyContinue)) { $k.adapterGoneS = $el }
        if ($null -eq $k.routesCleanS) {
            $left = @(Get-NetRoute -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceAlias -eq $TunName })
            if ($left.Count -eq 0) { $k.routesCleanS = $el }
        }
        if ($null -eq $k.internetS -and (Test-Tcp '1.1.1.1' 443 1000) -and (Test-Dns)) { $k.internetS = [math]::Round(((Get-Date) - $tk).TotalSeconds, 2) }
        if ($null -ne $k.adapterGoneS -and $null -ne $k.routesCleanS -and $null -ne $k.internetS) { break }
        Start-Sleep -Milliseconds 250
    }
    Start-Sleep -Seconds 5
    $after = Get-Snapshot
    $k.routeDiff = Compare-Sets $base.routes $after.routes
    $k.dnsDiff = Compare-Sets $base.dns $after.dns
    $k.wfpFiltersAfterKill = Get-WfpFilterCount
    $k.routesRestored = ($k.routeDiff.added.Count -eq 0 -and $k.routeDiff.removed.Count -eq 0)
    $D.kill = $k
    $within = { param($v) $null -ne $v -and $v -le 5 }
    $R.killRecovery = [bool]((& $within $k.adapterGoneS) -and (& $within $k.routesCleanS) -and (& $within $k.internetS) -and $k.routesRestored)
    $k | ConvertTo-Json -Depth 5 | Write-Host
    EndSection

    # ---------------------------------------------------------------- leftovers
    Section 'adapter leftovers'
    $ad = Compare-Sets $base.adapters $after.adapters
    $pn = Compare-Sets $base.pnp $after.pnp
    $D.leftovers = [ordered]@{ adapters = $ad; pnp = $pn }
    $R.adapterLeftovers = @($ad.added).Count + @($pn.added).Count
    $D.leftovers | ConvertTo-Json -Depth 5 | Write-Host
    EndSection
} catch {
    $D.error = "$($_.Exception.Message) @ $($_.InvocationInfo.PositionMessage)"
    Log "ERROR: $($D.error)"
    foreach ($n in @($Procs.Keys)) { Show-Log $n 80 }
} finally {
    & pktmon stop 2>&1 | Out-Null
    foreach ($n in @($Procs.Keys)) {
        $p = $Procs[$n]
        if ($p -and -not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue }
    }
    Save-Results
    $R | ConvertTo-Json -Depth 8 | Write-Host
}
if ($D.error) { exit 1 }
exit 0
