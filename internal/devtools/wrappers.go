package devtools

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Wrappers are our own files in BinDir, so they are not journaled: Enable
// (re)writes them and Disable deletes them. Each one reads the state file,
// sets the proxy variables when it holds a URL, and runs the real tool: the
// first one on PATH outside the wrapper's own directory. A wrapper never
// runs itself, even when BinDir is on PATH twice or in another spelling.

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
setlocal DisableDelayedExpansion
set "ZATVOR_PROXY="
if exist "`+state+`" set /p ZATVOR_PROXY=<"`+state+`"
if defined ZATVOR_PROXY set "HTTPS_PROXY=%ZATVOR_PROXY%"
if defined ZATVOR_PROXY set "HTTP_PROXY=%ZATVOR_PROXY%"
if defined ZATVOR_PROXY set "NO_PROXY=`+noProxy+`"
set "ZATVOR_PROXY="
for %%P in ("%~dp0.") do set "ZATVOR_SELF=%%~fsP"
set "ZATVOR_REAL="
for %%D in ("%PATH:;=" "%") do if not defined ZATVOR_REAL call :probe "%%~D"
set "ZATVOR_SELF="
if not defined ZATVOR_REAL goto notfound
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
$zatvorState = `+state+`
$zatvorProxy = ''
try { if ([IO.File]::Exists($zatvorState)) { $zatvorProxy = [IO.File]::ReadAllText($zatvorState).Trim() } } catch { }
if ($zatvorProxy) {
    $env:HTTPS_PROXY = $zatvorProxy
    $env:HTTP_PROXY = $zatvorProxy
    $env:NO_PROXY = '`+noProxy+`'
}
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
& $zatvorReal.Source @args
exit $LASTEXITCODE
`, "\n", "\r\n")
}

func shWrapper(tool, binDir, stateFile string) string {
	return `#!/bin/sh
# Zatvor wrapper for ` + tool + `: routes it through Zatvor while Zatvor is connected.
# Written by Zatvor and deleted when developer-tool routing is turned off.
zatvor_state=` + shQuote(posixPath(stateFile)) + `
zatvor_bin=` + shQuote(posixPath(binDir)) + `
zatvor_proxy=
if [ -r "$zatvor_state" ]; then IFS= read -r zatvor_proxy < "$zatvor_state" || :; fi
zatvor_cr=$(printf '\r')
zatvor_proxy=${zatvor_proxy%"$zatvor_cr"}
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
exec "$zatvor_real" "$@"
`
}
