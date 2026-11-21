#!/usr/bin/env bash
# Runs one prototype's --self-test on a CI runner and collects metrics.json.
# Usage (from the repo root, after the build step put the binary in out/):
#   proto/ui/selftest.sh tauri|wails
# Writes results/<framework>-<os>.json and results/<framework>-<os>-env.txt.
set -euo pipefail

fw=$1
mkdir -p results work
limit=300 # seconds; the self-test needs about 60 + 20 x 1.1 s plus start-up

case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*)
	os=windows
	exe=out/zatvor-proto-$fw.exe
	{
		echo "runner: $ImageOS $ImageVersion"
		powershell -NoProfile -Command '(Get-CimInstance Win32_OperatingSystem).Caption; (Get-CimInstance Win32_Processor).Name; (Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors; Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.Screen]::AllScreens | Format-List; Get-ChildItem "${env:ProgramFiles(x86)}\Microsoft\EdgeWebView\Application" -Directory -ErrorAction SilentlyContinue | ForEach-Object { "WebView2 runtime dir: $($_.Name)" }; "session: $([System.Diagnostics.Process]::GetCurrentProcess().SessionId)"; "interactive: $([Environment]::UserInteractive)"'
	} >"results/$fw-$os-env.txt" 2>&1 || true
	rm -f out/metrics.json
	start=$(date +%s)
	powershell -NoProfile -Command "\$p = Start-Process -FilePath '$exe' -ArgumentList '--self-test' -PassThru; if (-not \$p.WaitForExit(${limit}000)) { Stop-Process -Id \$p.Id -Force; Write-Output 'self-test timed out'; exit 1 }; Write-Output ('exit code ' + \$p.ExitCode)"
	echo "wall seconds: $(($(date +%s) - start))" >>"results/$fw-$os-env.txt"
	metrics=out/metrics.json
	;;
Darwin)
	os=macos
	case $fw in
	tauri) app=ZatvorProtoTauri.app exe=zatvor-proto-tauri ;;
	wails) app=ZatvorProtoWails.app exe=zatvor-proto-wails ;;
	esac
	{
		sw_vers
		sysctl -n machdep.cpu.brand_string hw.ncpu
		system_profiler SPDisplaysDataType 2>/dev/null | grep -E 'Resolution|Display Type|Main Display' || true
		defaults read -g AppleInterfaceStyle 2>/dev/null || echo "appearance: light"
	} >"results/$fw-$os-env.txt" 2>&1 || true
	rm -rf "work/$app"
	cp -R "out/$app" "work/$app" # metrics.json goes inside the copy, not the uploaded bundle
	start=$(date +%s)
	# LaunchServices start (like a user's double click), so that the app is its
	# own "responsible" process and WebKit helpers can be attributed to it.
	open -W -n "work/$app" --args --self-test &
	opener=$!
	while kill -0 $opener 2>/dev/null; do
		if (($(date +%s) - start > limit)); then
			pkill -f "work/$app/Contents/MacOS/$exe" || true
			echo "self-test timed out"
			exit 1
		fi
		sleep 2
	done
	echo "wall seconds: $(($(date +%s) - start))" >>"results/$fw-$os-env.txt"
	metrics=work/$app/Contents/MacOS/metrics.json
	;;
*)
	echo "unsupported OS" >&2
	exit 2
	;;
esac

if [ ! -s "$metrics" ]; then
	echo "no metrics.json at $metrics" >&2
	ls -la "$(dirname "$metrics")" >&2 || true
	exit 1
fi
cp "$metrics" "results/$fw-$os.json"
cat "results/$fw-$os.json"
