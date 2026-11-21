#!/bin/sh
# Zatvor one-line installer for macOS and Linux (spec section 11, R3.2).
#
# Verifies the Ed25519 signature of SHA256SUMS with the embedded release key
# (openssl pkeyutl -verify -rawin), then the SHA-256 of the release file
# against SHA256SUMS. Stage 0c prototype: there is no network release and no
# macOS/Linux package yet, so --source names a local directory and the script
# stops after verification; it changes nothing on the system.
#
# Needs an openssl with Ed25519 and -rawin (OpenSSL 3 or newer). macOS ships
# LibreSSL as /usr/bin/openssl; if the openssl found lacks Ed25519 the script
# refuses and says how to get one. OPENSSL=/path/to/openssl overrides.
#
# Usage: install.sh --source DIR --file NAME
# Exit codes: 0 verified; 1 verification failed; 2 bad usage;
#             3 openssl cannot verify Ed25519 (nothing was checked).

set -eu

# Release public key, PEM (SubjectPublicKeyInfo).
# TEST KEY -- replaced in stage 1 by Artur's release key. It is a throwaway key
# generated for stage 0c; its private half was discarded and signs nothing.
RELEASE_PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAkby01BOymxxHulOUnjH6AmNiibfnlrSbeXNICDlVE6E=
-----END PUBLIC KEY-----'

# Capability probe: RFC 8032 section 7.1 TEST 2 (public key, message 0x72,
# signature). A working Ed25519 openssl must accept it. TEST 1 is not used:
# its message is empty and OpenSSL 3.0 cannot verify an empty input with
# -rawin ("Could not allocate 0 bytes").
PROBE_PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAPUAXw+hDiVqStwqnTRt+vJyYLM8uxJaMwM1V8Sr0Zgw=
-----END PUBLIC KEY-----'
PROBE_MSG_HEX='72'
PROBE_SIG_HEX='92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00'

say() { printf 'install.sh: %s\n' "$*" >&2; }

usage() { say 'usage: install.sh --source DIR --file NAME'; exit 2; }

source_dir=''
file=''
while [ $# -gt 0 ]; do
	case "$1" in
	--source) [ $# -ge 2 ] || usage; source_dir=$2; shift 2 ;;
	--file) [ $# -ge 2 ] || usage; file=$2; shift 2 ;;
	*) usage ;;
	esac
done
[ -n "$source_dir" ] && [ -n "$file" ] || usage
case "$file" in
*/* | .* | '') say "bad file name: $file"; exit 2 ;;
esac

OPENSSL=${OPENSSL:-openssl}

work=$(mktemp -d "${TMPDIR:-/tmp}/zatvor-install.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM

# hex -> bytes with printf only (no xxd on minimal systems).
hex_to_file() {
	printf "$(printf '%s\n' "$1" | awk '{
		for (i = 1; i < length($0); i += 2) {
			b = (index("0123456789abcdef", substr($0, i, 1)) - 1) * 16 + index("0123456789abcdef", substr($0, i + 1, 1)) - 1
			printf "\\%03o", b
		}
	}')" >"$2"
}

refuse_openssl() {
	ver=$("$OPENSSL" version 2>/dev/null || echo 'not found')
	say "this openssl cannot verify Ed25519 signatures: $OPENSSL ($ver)"
	say 'it needs OpenSSL 3 or newer (pkeyutl -rawin with Ed25519). Nothing was checked or installed.'
	case "$(uname -s)" in
	Darwin)
		say 'macOS: brew install openssl@3, then rerun with'
		say '  OPENSSL="$(brew --prefix openssl@3)/bin/openssl" sh install.sh ...'
		;;
	*)
		say 'Linux: install OpenSSL 3 from your distribution (Debian/Ubuntu: apt install openssl), then rerun'
		say '  or point OPENSSL=/path/to/openssl at an OpenSSL 3+ binary.'
		;;
	esac
	exit 3
}

# 0. Can this openssl verify Ed25519 at all?
printf '%s\n' "$PROBE_PUBKEY_PEM" >"$work/probe.pem"
hex_to_file "$PROBE_MSG_HEX" "$work/probe.msg"
hex_to_file "$PROBE_SIG_HEX" "$work/probe.sig"
if ! "$OPENSSL" pkeyutl -verify -pubin -inkey "$work/probe.pem" -rawin \
	-in "$work/probe.msg" -sigfile "$work/probe.sig" >/dev/null 2>&1; then
	refuse_openssl
fi

# "Download": stage 0c copies from a local directory.
for name in SHA256SUMS SHA256SUMS.sig "$file"; do
	if [ ! -f "$source_dir/$name" ]; then
		say "missing $name in $source_dir"
		exit 1
	fi
	cp "$source_dir/$name" "$work/$name"
done

# 1. Signature of SHA256SUMS.
printf '%s\n' "$RELEASE_PUBKEY_PEM" >"$work/release.pem"
if ! "$OPENSSL" pkeyutl -verify -pubin -inkey "$work/release.pem" -rawin \
	-in "$work/SHA256SUMS" -sigfile "$work/SHA256SUMS.sig" >/dev/null 2>&1; then
	say 'SHA256SUMS signature is NOT valid for the Zatvor release key; nothing was installed'
	exit 1
fi
say 'SHA256SUMS signature: valid'

# 2. Hash of the file, from the signed list (exactly one entry).
want=$(awk -v f="$file" '
	{ sub(/\r$/, "") }
	length($1) == 64 && $1 ~ /^[0-9a-fA-F]+$/ {
		name = substr($0, 67)
		if (substr($0, 65, 1) == " " && (substr($0, 66, 1) == " " || substr($0, 66, 1) == "*") && name == f) { n++; h = tolower($1) }
	}
	END { if (n == 1) print h; else print "count:" n + 0 }' "$work/SHA256SUMS")
case "$want" in
count:*) say "SHA256SUMS lists $file ${want#count:} times (want 1); nothing was installed"; exit 1 ;;
esac
got=$("$OPENSSL" dgst -sha256 -r "$work/$file" | awk '{ print tolower($1) }')
if [ "$got" != "$want" ]; then
	say "$file SHA-256 $got does not match SHA256SUMS ($want); nothing was installed"
	exit 1
fi
say "$file SHA-256: matches"

# 3. Install. Stage 0c has no macOS/Linux package yet.
say 'verified. Stage 0c has no macOS/Linux package to install yet; stopping here.'
exit 0
