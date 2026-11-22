package devtools

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Wrappers are our own files in BinDir, so they are not journaled: Enable
// (re)writes them and Disable deletes them. Each one reads the state file,
// sets the proxy variables when it holds exactly http://127.0.0.1:<port>
// (anything else counts as empty and is never parsed as code), and runs
// the real tool: the first one on PATH outside the wrapper's own directory.
// BinDir is skipped only for that lookup; the tool gets the PATH it was
// given. ZATVOR_WRAPPED counts how deep wrappers are nested: each wrapper
// reads it (empty or not a number = 0) and passes depth+1 to the tool, so a
// tool that runs itself again through PATH keeps working. A wrapper that
// receives depth 8 or more refuses with exit code 1: it is most likely
// starting itself (BinDir on PATH under a name the lookup did not catch),
// and this stops it well before cmd's batch stack overflows.

var wrappedTools = []string{"codex", "gemini"}

type wrapperFile struct {
	name string
	perm fs.FileMode
	body string
}

func wrapperFiles(tool, binDir, stateFile string) []wrapperFile {
	return []wrapperFile{
		{tool + ".cmd", 0o644, cmdWrapper(tool, binDir, stateFile)},
		{tool + ".ps1", 0o644, ps1Wrapper(tool, binDir, stateFile)},
		{tool, 0o755, shWrapper(tool, binDir, stateFile)},
	}
}

// relState returns the state file relative to BinDir as path segments when
// that path is plain ASCII: cmd.exe reads a batch file in the OEM code page
// and Windows PowerShell 5.1 a BOM-less script in the ANSI one, so a user
// name in Cyrillic must not appear in them literally.
func relState(binDir, stateFile string) ([]string, bool) {
	rel, err := filepath.Rel(binDir, stateFile)
	if err != nil || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || !isASCII(rel) {
		return nil, false
	}
	return strings.Split(rel, string(filepath.Separator)), true
}

func cmdWrapper(tool, binDir, stateFile string) string {
	state := strings.ReplaceAll(stateFile, "%", "%%")
	if seg, ok := relState(binDir, stateFile); ok {
		state = "%~dp0" + strings.ReplaceAll(strings.Join(seg, `\`), "%", "%%")
	}
	return strings.ReplaceAll(`@echo off
rem Zatvor wrapper for `+tool+`: routes it through Zatvor while Zatvor is connected.
rem Written by Zatvor and deleted when developer-tool routing is turned off.
rem The PATH scan below does not handle entries holding a double quote, a
rem percent sign, a caret or the wildcards * and ?: they are searched wrongly.
setlocal DisableDelayedExpansion
set "ZATVOR_PROXY="
if exist "`+state+`" set /p ZATVOR_PROXY=<"`+state+`"
rem The state line is only looked at through delayed expansion, which never
rem parses it: http://127.0.0.1:PORT with 1 to 5 digits is used, else nothing.
setlocal EnableDelayedExpansion
set "ZATVOR_PORT="
if defined ZATVOR_PROXY if "!ZATVOR_PROXY:~0,17!"=="http://127.0.0.1:" set "ZATVOR_PORT=!ZATVOR_PROXY:~17!"
if defined ZATVOR_PORT if not "!ZATVOR_PORT:~5!"=="" set "ZATVOR_PORT="
set "ZATVOR_REST=!ZATVOR_PORT!"
for %%C in (0 1 2 3 4 5 6 7 8 9) do if defined ZATVOR_REST set "ZATVOR_REST=!ZATVOR_REST:%%C=!"
if defined ZATVOR_REST set "ZATVOR_PORT="
rem The nesting depth, also read without parsing: digits only, else 0. Leading
rem zeros go first, so set /a reads decimal; then three or more digits are 99.
set "ZATVOR_D="
if defined ZATVOR_WRAPPED set "ZATVOR_D=!ZATVOR_WRAPPED!"
set "ZATVOR_REST=!ZATVOR_D!"
for %%C in (0 1 2 3 4 5 6 7 8 9) do if defined ZATVOR_REST set "ZATVOR_REST=!ZATVOR_REST:%%C=!"
if defined ZATVOR_REST set "ZATVOR_D="
if not defined ZATVOR_D set "ZATVOR_D=0"
for /f "tokens=* delims=0" %%N in ("!ZATVOR_D!") do set "ZATVOR_D=%%N"
if "!ZATVOR_D:0=!"=="" set "ZATVOR_D=0"
if not "!ZATVOR_D:~2!"=="" set "ZATVOR_D=99"
endlocal & set "ZATVOR_PORT=%ZATVOR_PORT%" & set "ZATVOR_DEPTH=%ZATVOR_D%"
set /a ZATVOR_DEPTH=ZATVOR_DEPTH
if %ZATVOR_DEPTH% GEQ 8 goto nested
set "ZATVOR_PROXY="
if defined ZATVOR_PORT set "HTTPS_PROXY=http://127.0.0.1:%ZATVOR_PORT%"
if defined ZATVOR_PORT set "HTTP_PROXY=http://127.0.0.1:%ZATVOR_PORT%"
if defined ZATVOR_PORT set "NO_PROXY=`+noProxy+`"
set "ZATVOR_PORT="
for %%P in ("%~dp0.") do set "ZATVOR_SELF=%%~fsP"
set "ZATVOR_REAL="
for %%D in ("%PATH:;=" "%") do if not defined ZATVOR_REAL call :probe "%%~D"
set "ZATVOR_SELF="
if not defined ZATVOR_REAL goto notfound
set /a ZATVOR_WRAPPED=ZATVOR_DEPTH+1
set "ZATVOR_DEPTH="
if /i "%ZATVOR_REAL:~-4%"==".cmd" goto batch
if /i "%ZATVOR_REAL:~-4%"==".bat" goto batch
"%ZATVOR_REAL%" %*
exit /b %ERRORLEVEL%
:batch
call "%ZATVOR_REAL%" %*
exit /b %ERRORLEVEL%
:notfound
>&2 echo zatvor: `+tool+` was not found on PATH outside %~dp0
exit /b 9009
:nested
>&2 echo zatvor: the `+tool+` wrapper is nested %ZATVOR_DEPTH% deep, so it is probably starting itself; check that its folder is not on PATH under another name
exit /b 1
:probe
if "%~1"=="" exit /b 0
for %%P in ("%~1\.") do if /i "%%~fsP"=="%ZATVOR_SELF%" exit /b 0
for %%E in (%PATHEXT%) do if not defined ZATVOR_REAL if exist "%~1\`+tool+`%%E" set "ZATVOR_REAL=%~1\`+tool+`%%E"
exit /b 0
`, "\n", "\r\n")
}

func ps1Wrapper(tool, binDir, stateFile string) string {
	state := pwshString(stateFile)
	if seg, ok := relState(binDir, stateFile); ok {
		q := make([]string, len(seg))
		for i, s := range seg {
			q[i] = pwshString(s)
		}
		state = "[IO.Path]::GetFullPath([IO.Path]::Combine($PSScriptRoot, " + strings.Join(q, ", ") + "))"
	}
	return strings.ReplaceAll(`# Zatvor wrapper for `+tool+`: routes it through Zatvor while Zatvor is connected.
# Written by Zatvor and deleted when developer-tool routing is turned off.
$zatvorDepth = 0
if ("$env:ZATVOR_WRAPPED" -match '^0*([0-9]{1,9})$') { $zatvorDepth = [int]$Matches[1] }
elseif ("$env:ZATVOR_WRAPPED" -match '^[0-9]+$') { $zatvorDepth = 99 }
if ($zatvorDepth -ge 8) {
    [Console]::Error.WriteLine("zatvor: the `+tool+` wrapper is nested $zatvorDepth deep, so it is probably starting itself; check that its folder is not on PATH under another name")
    exit 1
}
$zatvorState = `+state+`
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
    $zatvorReal = Get-Command -Name '`+tool+`' -CommandType Application, ExternalScript -ErrorAction SilentlyContinue |
        Where-Object { [IO.Path]::GetDirectoryName($_.Source).TrimEnd('\', '/') -ne $zatvorSelf } |
        Select-Object -First 1
} finally {
    $env:PATH = $zatvorPath
}
if (-not $zatvorReal) {
    [Console]::Error.WriteLine("zatvor: `+tool+` was not found on PATH outside $zatvorSelf")
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
        $env:NO_PROXY = '`+noProxy+`'
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
`, "\n", "\r\n")
}

func shWrapper(tool, binDir, stateFile string) string {
	return `#!/bin/sh
# Zatvor wrapper for ` + tool + `: routes it through Zatvor while Zatvor is connected.
# Written by Zatvor and deleted when developer-tool routing is turned off.
zatvor_depth=${ZATVOR_WRAPPED-}
case $zatvor_depth in
  ''|*[!0-9]*) zatvor_depth=0 ;;
  *) zatvor_depth=${zatvor_depth#"${zatvor_depth%%[!0]*}"}; zatvor_depth=${zatvor_depth:-0} ;;
esac
case $zatvor_depth in ???*) zatvor_depth=99 ;; esac
if [ "$zatvor_depth" -ge 8 ]; then
  echo "zatvor: the ` + tool + ` wrapper is nested $zatvor_depth deep, so it is probably starting itself; check that its folder is not on PATH under another name" >&2
  exit 1
fi
zatvor_state=` + shQuote(posixPath(stateFile)) + `
zatvor_bin=` + shQuote(posixPath(binDir)) + `
zatvor_proxy=
if [ -r "$zatvor_state" ]; then IFS= read -r zatvor_proxy < "$zatvor_state" || :; fi
zatvor_cr=$(printf '\r')
zatvor_proxy=${zatvor_proxy%"$zatvor_cr"}
case $zatvor_proxy in
  http://127.0.0.1:*) case ${zatvor_proxy#http://127.0.0.1:} in ''|*[!0-9]*|??????*) zatvor_proxy= ;; esac ;;
  *) zatvor_proxy= ;;
esac
if [ -n "$zatvor_proxy" ]; then
  HTTPS_PROXY=$zatvor_proxy HTTP_PROXY=$zatvor_proxy NO_PROXY='` + noProxy + `'
  export HTTPS_PROXY HTTP_PROXY NO_PROXY
fi
zatvor_real=
zatvor_ifs=$IFS
IFS=:
set -f
for zatvor_d in $PATH; do
  [ -n "$zatvor_d" ] || continue
  [ "${zatvor_d%/}" = "${zatvor_bin%/}" ] && continue
  [ "$zatvor_d" -ef "$zatvor_bin" ] && continue
  [ "$zatvor_d/` + tool + `" -ef "$0" ] && continue
  if [ -f "$zatvor_d/` + tool + `" ] && [ -x "$zatvor_d/` + tool + `" ]; then
    zatvor_real=$zatvor_d/` + tool + `
    break
  fi
done
set +f
IFS=$zatvor_ifs
if [ -z "$zatvor_real" ]; then
  echo "zatvor: ` + tool + ` was not found on PATH outside $zatvor_bin" >&2
  exit 127
fi
ZATVOR_WRAPPED=$((zatvor_depth + 1))
export ZATVOR_WRAPPED
exec "$zatvor_real" "$@"
`
}
