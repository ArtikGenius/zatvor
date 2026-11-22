# Zatvor wrapper for codex: routes it through Zatvor while Zatvor is connected.
# Written by Zatvor and deleted when developer-tool routing is turned off.
$zatvorDepth = 0
if ("$env:ZATVOR_WRAPPED" -match '^0*([0-9]{1,9})$') { $zatvorDepth = [int]$Matches[1] }
elseif ("$env:ZATVOR_WRAPPED" -match '^[0-9]+$') { $zatvorDepth = 99 }
if ($zatvorDepth -ge 8) {
    [Console]::Error.WriteLine("zatvor: the codex wrapper is nested $zatvorDepth deep, so it is probably starting itself; check that its folder is not on PATH under another name")
    exit 1
}
$zatvorState = [IO.Path]::GetFullPath([IO.Path]::Combine($PSScriptRoot, '..', 'run', 'devtools.proxy'))
$zatvorProxy = ''
try { if ([IO.File]::Exists($zatvorState)) { $zatvorProxy = [IO.File]::ReadAllText($zatvorState).Trim() } } catch { }
if ($zatvorProxy -cnotmatch '^http://127\.0\.0\.1:[0-9]{1,5}$') { $zatvorProxy = '' }
$zatvorSelf = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\', '/')
$zatvorPath = $env:PATH
$zatvorReal = $null
try {
    $env:PATH = (($zatvorPath -split [IO.Path]::PathSeparator) | Where-Object {
        $_ -and $(try { [IO.Path]::GetFullPath($_).TrimEnd('\', '/') -ne $zatvorSelf } catch { $true })
    }) -join [IO.Path]::PathSeparator
    $zatvorReal = Get-Command -Name 'codex' -CommandType Application, ExternalScript -ErrorAction SilentlyContinue |
        Where-Object { [IO.Path]::GetDirectoryName($_.Source).TrimEnd('\', '/') -ne $zatvorSelf } |
        Select-Object -First 1
} finally {
    $env:PATH = $zatvorPath
}
if (-not $zatvorReal) {
    [Console]::Error.WriteLine("zatvor: codex was not found on PATH outside $zatvorSelf")
    exit 127
}
# The wrapper can run inside the user's PowerShell session: put back the
# variables it sets once the tool is done.
$zatvorSaved = @{}
foreach ($zatvorName in 'HTTPS_PROXY', 'HTTP_PROXY', 'NO_PROXY', 'ZATVOR_WRAPPED') {
    $zatvorSaved[$zatvorName] = [Environment]::GetEnvironmentVariable($zatvorName)
}
$zatvorCode = 0
try {
    if ($zatvorProxy) {
        $env:HTTPS_PROXY = $zatvorProxy
        $env:HTTP_PROXY = $zatvorProxy
        $env:NO_PROXY = 'localhost,127.0.0.1,::1'
    }
    $env:ZATVOR_WRAPPED = [string]($zatvorDepth + 1)
    & $zatvorReal.Source @args
    $zatvorCode = $LASTEXITCODE
} finally {
    foreach ($zatvorName in $zatvorSaved.Keys) {
        [Environment]::SetEnvironmentVariable($zatvorName, $zatvorSaved[$zatvorName])
    }
}
exit $zatvorCode
