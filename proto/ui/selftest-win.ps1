# Starts a prototype with --self-test, and 50 s in (popup still hidden, before
# the self-test's own 60 s reading) records the private working set of its
# process tree (Task Manager's "Memory" column) from the perf counters.
param(
    [Parameter(Mandatory)] [string] $Exe,
    [Parameter(Mandatory)] [string] $Out,
    [int] $Limit = 300
)
$ErrorActionPreference = 'Stop'
$p = Start-Process -FilePath $Exe -ArgumentList '--self-test' -PassThru
Start-Sleep -Seconds 50
$all = Get-CimInstance Win32_PerfFormattedData_PerfProc_Process | Where-Object { $_.Name -ne '_Total' -and $_.Name -ne 'Idle' }
$tree = @($p.Id)
for ($i = 0; $i -lt $tree.Count; $i++) {
    foreach ($c in $all | Where-Object { $_.CreatingProcessID -eq $tree[$i] -and $_.IDProcess -ne 0 }) {
        if ($tree -notcontains [int]$c.IDProcess) { $tree += [int]$c.IDProcess }
    }
}
$procs = foreach ($id in $tree) {
    $r = $all | Where-Object { $_.IDProcess -eq $id } | Select-Object -First 1
    if ($r) {
        [pscustomobject]@{ pid = $id; name = $r.Name; ppid = $r.CreatingProcessID
            privateWorkingSetMB = [math]::Round($r.WorkingSetPrivate / 1MB, 1)
            workingSetMB = [math]::Round($r.WorkingSet / 1MB, 1)
            privateBytesMB = [math]::Round($r.PrivateBytes / 1MB, 1) }
    }
}
$sum = ($procs | Measure-Object -Property privateWorkingSetMB -Sum).Sum
[pscustomobject]@{
    when = 'hidden, 50 s after start'
    hiddenPrivateWorkingSetMB = [math]::Round($sum, 1)
    processes = @($procs)
} | ConvertTo-Json -Depth 4 | Set-Content -Encoding utf8 $Out
Get-Content $Out
if (-not $p.WaitForExit(($Limit - 50) * 1000)) {
    Stop-Process -Id $p.Id -Force
    Write-Output 'self-test timed out'
    exit 1
}
Write-Output "exit code $($p.ExitCode)"
