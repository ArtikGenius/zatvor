#!/usr/bin/env bash
# CI only (macOS runner): checks the interactive path of one prototype.
#  1. --ui-check: opens the popup the way a tray click does, clicks every
#     control from the page, writes ui-check.json; fails if any check fails.
#  2. Normal start: the popup must open by itself; a screenshot is kept.
# Usage (from the repo root, after selftest.sh): proto/ui/uicheck.sh tauri|wails
# Writes results/<fw>-macos-ui-check.json, -ui.log, -ui-check.png, -start.png.
set -euo pipefail
[ "${CI:-}" = true ] || { echo "uicheck.sh: CI only" >&2; exit 2; }

fw=$1
case $fw in
tauri) app=ZatvorProtoTauri.app exe=zatvor-proto-tauri ;;
wails) app=ZatvorProtoWails.app exe=zatvor-proto-wails ;;
*) echo "usage: $0 tauri|wails" >&2; exit 2 ;;
esac
log="$HOME/Library/Logs/ZatvorProto/$fw.log"
mkdir -p results work
rm -rf "work/ui-$app" "$log"
cp -R "out/$app" "work/ui-$app"
bin="work/ui-$app/Contents/MacOS/$exe"

wait_log() { # pattern seconds
	local end=$(($(date +%s) + $2))
	while (($(date +%s) < end)); do
		grep -q "$1" "$log" 2>/dev/null && return 0
		sleep 0.5
	done
	return 1
}

# 1. --ui-check (the app holds the popup 8 s after the last click for the screenshot)
ZATVOR_UI_CHECK_HOLD=1 open -W -n "work/ui-$app" --args --ui-check &
opener=$!
if wait_log "click theme=system" 60; then
	sleep 1
	screencapture -x "results/$fw-macos-ui-check.png" || echo "screencapture failed"
fi
wait $opener || true
cp "$log" "results/$fw-macos-ui.log" || true
res="work/ui-$app/Contents/MacOS/ui-check.json"
echo "--- $log"; cat "$log" || true; echo "---"
if [ ! -s "$res" ]; then
	echo "no ui-check.json" >&2
	cat "$log" >&2 || true
	exit 1
fi
cp "$res" "results/$fw-macos-ui-check.json"
cat "$res"
python3 - "$res" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
bad = [k for k in ("pageLoaded", "visible", "onScreen") if r.get(k) is not True]
bad += [k for k, v in r.get("clicks", {}).items() if v is not True]
if bad:
    sys.exit("ui-check failed: " + ", ".join(bad))
print("ui-check passed")
PY

# 2. normal start: the popup opens by itself (the process is ours, stopped by PID)
: >"$log"
"$bin" &
pid=$!
if wait_log "popup shown" 30; then
	sleep 1.5
	screencapture -x "results/$fw-macos-start.png" || echo "screencapture failed"
	ok=1
else
	ok=0
fi
kill "$pid" 2>/dev/null || true
wait "$pid" 2>/dev/null || true
cat "$log" >>"results/$fw-macos-ui.log"
[ $ok = 1 ] || { echo "popup did not open at start" >&2; exit 1; }
echo "start check passed"
