@echo off
rem Zatvor wrapper for codex: routes it through Zatvor while Zatvor is connected.
rem Written by Zatvor and deleted when developer-tool routing is turned off.
rem The PATH scan below does not handle entries holding a double quote, a
rem percent sign, a caret or the wildcards * and ?: they are searched wrongly.
setlocal DisableDelayedExpansion
set "ZATVOR_PROXY="
if exist "%~dp0..\run\devtools.proxy" set /p ZATVOR_PROXY=<"%~dp0..\run\devtools.proxy"
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
rem A cmd quirk: a substring or replace expansion (X:~2, X:0=) of an UNDEFINED
rem X gives literal text, not nothing, and set "X=" undefines X. So every such
rem use sits behind if defined X, or right after if not defined X set "X=0".
set "ZATVOR_D="
if defined ZATVOR_WRAPPED set "ZATVOR_D=!ZATVOR_WRAPPED!"
set "ZATVOR_REST=!ZATVOR_D!"
for %%C in (0 1 2 3 4 5 6 7 8 9) do if defined ZATVOR_REST set "ZATVOR_REST=!ZATVOR_REST:%%C=!"
if defined ZATVOR_REST set "ZATVOR_D="
if not defined ZATVOR_D set "ZATVOR_D=0"
if "!ZATVOR_D:0=!"=="" set "ZATVOR_D=0"
for /f "tokens=* delims=0" %%N in ("!ZATVOR_D!") do set "ZATVOR_D=%%N"
if not defined ZATVOR_D set "ZATVOR_D=0"
if not "!ZATVOR_D:~2!"=="" set "ZATVOR_D=99"
endlocal & set "ZATVOR_PORT=%ZATVOR_PORT%" & set "ZATVOR_DEPTH=%ZATVOR_D%"
set /a ZATVOR_DEPTH=ZATVOR_DEPTH
if %ZATVOR_DEPTH% GEQ 8 goto nested
set "ZATVOR_PROXY="
if defined ZATVOR_PORT set "HTTPS_PROXY=http://127.0.0.1:%ZATVOR_PORT%"
if defined ZATVOR_PORT set "HTTP_PROXY=http://127.0.0.1:%ZATVOR_PORT%"
if defined ZATVOR_PORT set "NO_PROXY=localhost,127.0.0.1,::1"
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
>&2 echo zatvor: codex was not found on PATH outside %~dp0
exit /b 9009
:nested
>&2 echo zatvor: the codex wrapper is nested %ZATVOR_DEPTH% deep, so it is probably starting itself; check that its folder is not on PATH under another name
exit /b 1
:probe
if "%~1"=="" exit /b 0
for %%P in ("%~1\.") do if /i "%%~fsP"=="%ZATVOR_SELF%" exit /b 0
for %%E in (%PATHEXT%) do if not defined ZATVOR_REAL if exist "%~1\codex%%E" set "ZATVOR_REAL=%~1\codex%%E"
exit /b 0
