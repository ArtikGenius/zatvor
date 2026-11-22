# >>> zatvor >>>
# Managed by Zatvor: while Zatvor is connected, new prompts route developer
# tools through it. Zatvor removes this block when the feature is turned off.
$global:__zatvorState = 'C:\Users\test\AppData\Local\Zatvor\Run\devtools.proxy'
$global:__zatvorStamp = $null
$global:__zatvorProxy = $null
if ("$function:prompt" -notmatch '__zatvorSync') { $global:__zatvorPrompt = $function:prompt }
if (-not $global:__zatvorPrompt) { $global:__zatvorPrompt = { "PS $($executionContext.SessionState.Path.CurrentLocation)$('>' * ($nestedPromptLevel + 1)) " } }
function global:__zatvorSync {
    $stamp = $null
    try { if ([IO.File]::Exists($global:__zatvorState)) { $stamp = [IO.File]::GetLastWriteTimeUtc($global:__zatvorState).Ticks } } catch { }
    if ($stamp -eq $global:__zatvorStamp) { return }
    $global:__zatvorStamp = $stamp
    $p = ''
    if ($null -ne $stamp) { try { $p = [IO.File]::ReadAllText($global:__zatvorState).Trim() } catch { $p = '' } }
    if ($p -cnotmatch '^http://127\.0\.0\.1:[0-9]{1,5}$') { $p = '' }
    if ($p) {
        $env:HTTPS_PROXY = $p
        $env:HTTP_PROXY = $p
        $env:NO_PROXY = 'localhost,127.0.0.1,::1'
        $global:__zatvorProxy = $p
    } elseif ($global:__zatvorProxy) {
        if ($env:HTTPS_PROXY -eq $global:__zatvorProxy) { Remove-Item Env:HTTPS_PROXY -ErrorAction SilentlyContinue }
        if ($env:HTTP_PROXY -eq $global:__zatvorProxy) { Remove-Item Env:HTTP_PROXY -ErrorAction SilentlyContinue }
        if ($env:NO_PROXY -eq 'localhost,127.0.0.1,::1') { Remove-Item Env:NO_PROXY -ErrorAction SilentlyContinue }
        $global:__zatvorProxy = $null
    }
}
function global:prompt {
    $__zatvorOut = & $global:__zatvorPrompt
    __zatvorSync
    $__zatvorOut
}
__zatvorSync
# <<< zatvor <<<
