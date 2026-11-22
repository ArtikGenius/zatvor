#!/bin/sh
# Tests for scripts/install/install.sh (stage 0c, task 10). Changes nothing on
# the system: install.sh stops after verification in 0c.
#
# SIGNER_OPENSSL: an OpenSSL 3 used to make the throwaway key and signature
# for this run (default: openssl). The key lives in a temp dir and is deleted.
# Prints one line per case and "default openssl: ..." for the report.

set -eu

here=$(cd "$(dirname "$0")" && pwd)
script="$here/../install.sh"
SIGNER_OPENSSL=${SIGNER_OPENSSL:-openssl}

t=$(mktemp -d "${TMPDIR:-/tmp}/zatvor-sh-test.XXXXXX")
trap 'rm -rf "$t"' EXIT

# Throwaway key pair and a signed test release.
"$SIGNER_OPENSSL" genpkey -algorithm ed25519 -out "$t/key.pem"
"$SIGNER_OPENSSL" pkey -in "$t/key.pem" -pubout -out "$t/pub.pem"
mkdir "$t/rel"
head -c 4096 /dev/urandom >"$t/rel/zatvor-0.0.1.tar.gz"
(cd "$t/rel" && "$SIGNER_OPENSSL" dgst -sha256 -r zatvor-0.0.1.tar.gz | awk '{ print tolower($1) "  zatvor-0.0.1.tar.gz" }' >SHA256SUMS)
printf '%s  %s\n' "$(printf '0%.0s' $(seq 1 64))" other-file.zip >>"$t/rel/SHA256SUMS"
"$SIGNER_OPENSSL" pkeyutl -sign -inkey "$t/key.pem" -rawin -in "$t/rel/SHA256SUMS" -out "$t/rel/SHA256SUMS.sig"
rm -f "$t/key.pem"

# install.sh with the per-run key in place of the committed placeholder.
awk -v pubfile="$t/pub.pem" '
	/^RELEASE_PUBKEY_PEM=/ { skip = 1; printf "RELEASE_PUBKEY_PEM=\x27"; first = 1
		while ((getline l < pubfile) > 0) { if (!first) printf "\n"; printf "%s", l; first = 0 }
		print "\x27"; next }
	skip && /^-----END PUBLIC KEY-----/ { skip = 0; next }
	skip { next }
	{ print }' "$script" >"$t/install.sh"
grep -q 'MCowBQYDK2VwAyEAkby01BOymxxHulOUnjH6AmNiibfnlrSbeXNICDlVE6E=' "$t/install.sh" && { echo 'key not replaced'; exit 1; }

# An openssl that lacks Ed25519 / -rawin, like old LibreSSL.
mkdir "$t/shim"
cat >"$t/shim/openssl" <<'EOF'
#!/bin/sh
case "$1" in
version) echo 'LibreSSL 2.8.3 (test shim without Ed25519)' ;;
*) echo "$1: Unknown option: -rawin" >&2; exit 1 ;;
esac
EOF
chmod +x "$t/shim/openssl"

fails=0
# check NAME WANT_EXIT [GREP] -- command...
check() {
	name=$1 want=$2 pattern=$3
	shift 3
	set +e
	out=$("$@" 2>&1)
	code=$?
	set -e
	ok=yes
	[ "$code" -eq "$want" ] || ok=no
	if [ -n "$pattern" ] && ! printf '%s' "$out" | grep -q -- "$pattern"; then ok=no; fi
	if [ $ok = yes ]; then r=PASS; else r=FAIL; fails=$((fails + 1)); fi
	printf '%-44s exit %s (want %s) %s\n' "$name" "$code" "$want" "$r"
	[ $ok = yes ] || printf '%s\n' "$out" | sed 's/^/    | /'
}

copy() { rm -rf "$t/$1"; cp -R "$t/rel" "$t/$1"; }

export OPENSSL="$SIGNER_OPENSSL"
check 'valid release' 0 'SHA-256: matches' sh "$t/install.sh" --source "$t/rel" --file zatvor-0.0.1.tar.gz
check 'committed placeholder key' 1 'NOT valid' sh "$script" --source "$t/rel" --file zatvor-0.0.1.tar.gz
copy badsig
# flip one bit in byte 10 of the signature
b=$(od -An -tu1 -j10 -N1 "$t/rel/SHA256SUMS.sig" | tr -d ' ')
{ head -c 10 "$t/rel/SHA256SUMS.sig"; printf "\\$(printf '%03o' $((b ^ 1)))"; tail -c 53 "$t/rel/SHA256SUMS.sig"; } >"$t/badsig/SHA256SUMS.sig"
cmp -s "$t/rel/SHA256SUMS.sig" "$t/badsig/SHA256SUMS.sig" && { echo 'signature not changed'; exit 1; }
check 'signature bit flipped' 1 'NOT valid' sh "$t/install.sh" --source "$t/badsig" --file zatvor-0.0.1.tar.gz
copy badsums
printf '%s  extra.zip\n' "$(printf '1%.0s' $(seq 1 64))" >>"$t/badsums/SHA256SUMS"
check 'SHA256SUMS changed after signing' 1 'NOT valid' sh "$t/install.sh" --source "$t/badsums" --file zatvor-0.0.1.tar.gz
copy badfile
printf 'x' >>"$t/badfile/zatvor-0.0.1.tar.gz"
check 'file changed (hash mismatch)' 1 'does not match' sh "$t/install.sh" --source "$t/badfile" --file zatvor-0.0.1.tar.gz
check 'file not listed in SHA256SUMS' 1 '0 times' sh -c "cp '$t/rel/zatvor-0.0.1.tar.gz' '$t/rel/unlisted.tar.gz' && sh '$t/install.sh' --source '$t/rel' --file unlisted.tar.gz"
check 'missing arguments' 2 'usage' sh "$t/install.sh" --source "$t/rel"
case "$(uname -s)" in Darwin) hint='brew install openssl@3' ;; *) hint='OpenSSL 3' ;; esac
check 'openssl without Ed25519 (PATH shim)' 3 "$hint" env -u OPENSSL PATH="$t/shim:$PATH" sh "$t/install.sh" --source "$t/rel" --file zatvor-0.0.1.tar.gz

# The openssl a user would get by default, for the report (either branch is
# acceptable: 0 = it verifies, 3 = refused with instructions).
default=$(env -u OPENSSL sh -c 'command -v openssl' || echo none)
set +e
env -u OPENSSL sh "$t/install.sh" --source "$t/rel" --file zatvor-0.0.1.tar.gz >"$t/default.out" 2>&1
dcode=$?
set -e
echo "default openssl: $default ($(env -u OPENSSL openssl version 2>/dev/null || echo 'n/a')) -> install.sh exit $dcode"
sed 's/^/    | /' "$t/default.out"
case $dcode in 0 | 3) ;; *) echo 'default openssl: unexpected exit'; fails=$((fails + 1)) ;; esac

[ "$fails" -eq 0 ] || { echo "$fails case(s) failed"; exit 1; }
echo 'all cases passed'
