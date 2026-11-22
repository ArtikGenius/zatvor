package devtools

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf16"
)

// The marked block in a shell profile. Only the text between (and including)
// these lines is ours; everything else in the file is the user's.
const (
	beginMarker = "# >>> zatvor >>>"
	endMarker   = "# <<< zatvor <<<"
	noProxy     = "localhost,127.0.0.1,::1"
)

var errUnterminated = errors.New("devtools: the zatvor block has no end marker")

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pwshString is a PowerShell expression for s that is plain ASCII, because
// Windows PowerShell 5.1 reads a BOM-less script in the ANSI code page: a
// non-ASCII path (a Cyrillic user name) is decoded from base64 instead.
func pwshString(s string) string {
	if !isASCII(s) {
		return "[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString([]byte(s)) + "'))"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// posixPath turns a drive path (C:\a\b) into the form Git Bash uses (/c/a/b);
// other paths are returned unchanged.
func posixPath(p string) string {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') {
		return "/" + strings.ToLower(p[:1]) + strings.ReplaceAll(p[2:], `\`, "/")
	}
	return p
}

// pwshBlock is the PowerShell profile block. It wraps the prompt function the
// profile defined so far (or the default prompt) and, at every prompt, looks
// at the state file's LastWriteTime; only when that changed does it read the
// file and set or clear the proxy variables. It clears only values it set.
// The user's prompt runs first, so it still sees the last command's $? (the
// error marks of Starship and oh-my-posh). Anything in the state file but
// exactly http://127.0.0.1:<port> counts as empty. The block must stay last
// in the profile: a prompt function defined after it replaces the wrapper.
func pwshBlock(stateFile string) string {
	return beginMarker + `
# Managed by Zatvor: while Zatvor is connected, new prompts route developer
# tools through it. Zatvor removes this block when the feature is turned off.
$global:__zatvorState = ` + pwshString(stateFile) + `
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
        $env:NO_PROXY = '` + noProxy + `'
        $global:__zatvorProxy = $p
    } elseif ($global:__zatvorProxy) {
        if ($env:HTTPS_PROXY -eq $global:__zatvorProxy) { Remove-Item Env:HTTPS_PROXY -ErrorAction SilentlyContinue }
        if ($env:HTTP_PROXY -eq $global:__zatvorProxy) { Remove-Item Env:HTTP_PROXY -ErrorAction SilentlyContinue }
        if ($env:NO_PROXY -eq '` + noProxy + `') { Remove-Item Env:NO_PROXY -ErrorAction SilentlyContinue }
        $global:__zatvorProxy = $null
    }
}
function global:prompt {
    $__zatvorOut = & $global:__zatvorPrompt
    __zatvorSync
    $__zatvorOut
}
__zatvorSync
` + endMarker + "\n"
}

// bashBlock is the bash profile block: a PROMPT_COMMAND hook, appended to
// the user's own, that reads the state file with the read builtin (no fork)
// and sets or clears the proxy variables when the value changed. It clears
// only values it set; anything but http://127.0.0.1:<port> counts as empty.
func bashBlock(stateFile string) string {
	return beginMarker + `
# Managed by Zatvor: while Zatvor is connected, new prompts route developer
# tools through it. Zatvor removes this block when the feature is turned off.
__zatvor_state=` + shQuote(posixPath(stateFile)) + `
__zatvor_last=
__zatvor_set=
__zatvor_sync() {
  local p=
  if [ -r "$__zatvor_state" ]; then IFS= read -r p < "$__zatvor_state" || :; fi
  p=${p%$'\r'}
  case $p in
    http://127.0.0.1:*) case ${p#http://127.0.0.1:} in ''|*[!0-9]*|??????*) p= ;; esac ;;
    *) p= ;;
  esac
  [ "$p" = "$__zatvor_last" ] && return 0
  __zatvor_last=$p
  if [ -n "$p" ]; then
    export HTTPS_PROXY="$p" HTTP_PROXY="$p" NO_PROXY='` + noProxy + `'
    __zatvor_set=$p
  elif [ -n "$__zatvor_set" ]; then
    [ "${HTTPS_PROXY-}" = "$__zatvor_set" ] && unset HTTPS_PROXY
    [ "${HTTP_PROXY-}" = "$__zatvor_set" ] && unset HTTP_PROXY
    [ "${NO_PROXY-}" = '` + noProxy + `' ] && unset NO_PROXY
    __zatvor_set=
  fi
  return 0
}
case "${PROMPT_COMMAND-}" in
  *__zatvor_sync*) ;;
  *) PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND$'\n'}__zatvor_sync" ;;
esac
__zatvor_sync
` + endMarker + "\n"
}

// A profile is held as code units: bytes for UTF-8 and ANSI files, uint16
// for UTF-16LE ones, so that every byte we do not own is written back as
// it was read.
type unit interface{ ~byte | ~uint16 }

func units[E unit](s string) []E { // s is ASCII, or a UTF-8 string for uint16
	var zero E
	if _, wide := any(zero).(uint16); wide {
		u := utf16.Encode([]rune(s))
		out := make([]E, len(u))
		for i, c := range u {
			out[i] = E(c)
		}
		return out
	}
	out := make([]E, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = E(s[i])
	}
	return out
}

func text[E unit](s []E) string {
	var zero E
	if _, wide := any(zero).(uint16); wide {
		u := make([]uint16, len(s))
		for i, c := range s {
			u[i] = uint16(c)
		}
		return string(utf16.Decode(u))
	}
	b := make([]byte, len(s))
	for i, c := range s {
		b[i] = byte(c)
	}
	return string(b)
}

// lineIs reports whether line (without its LF) is marker, ignoring a CR and
// trailing blanks.
func lineIs[E unit](line []E, marker string) bool {
	for len(line) > 0 && (line[len(line)-1] == '\r' || line[len(line)-1] == ' ' || line[len(line)-1] == '\t') {
		line = line[:len(line)-1]
	}
	if len(line) != len(marker) {
		return false
	}
	for i := range line {
		if line[i] != E(marker[i]) {
			return false
		}
	}
	return true
}

// findBlock returns the block's span: from the start of the begin marker
// line to just after the end marker line's LF (or the end of s).
func findBlock[E unit](s []E) (start, end int, found bool, err error) {
	start = -1
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] != '\n' {
			j++
		}
		next := min(j+1, len(s))
		switch {
		case start < 0 && lineIs(s[i:j], beginMarker):
			start = i
		case start >= 0 && lineIs(s[i:j], endMarker):
			return start, next, true, nil
		}
		i = next
	}
	if start >= 0 {
		return 0, 0, false, errUnterminated
	}
	return 0, 0, false, nil
}

// newlineOf returns the file's newline: that of its first line, else def.
func newlineOf[E unit](s []E, def string) string {
	for i, c := range s {
		if c == '\n' {
			if i > 0 && s[i-1] == '\r' {
				return "\r\n"
			}
			return "\n"
		}
	}
	return def
}

// blockOf returns our block's text with LF newlines, or "" if there is none.
func blockOf[E unit](s []E) (string, error) {
	a, b, ok, err := findBlock(s)
	if err != nil || !ok {
		return "", err
	}
	t := strings.ReplaceAll(text(s[a:b]), "\r\n", "\n")
	return strings.TrimRight(t, "\r\n") + "\n", nil
}

// withBlock returns s with our block set to block (LF newlines; "" removes
// it). A new block is appended after one newline of separation, which the
// removal takes away again, so add-then-remove is byte-exact.
func withBlock[E unit](s []E, block, defNL string) ([]E, error) {
	nl := newlineOf(s, defNL)
	a, b, found, err := findBlock(s)
	if err != nil {
		return nil, err
	}
	ins := units[E](strings.ReplaceAll(block, "\n", nl))
	out := make([]E, 0, len(s)+len(ins)+2)
	switch {
	case found && block == "":
		cut := a
		if cut > 0 && s[cut-1] == '\n' {
			cut--
			if cut > 0 && s[cut-1] == '\r' {
				cut--
			}
		}
		out = append(append(out, s[:cut]...), s[b:]...)
	case found:
		out = append(append(append(out, s[:a]...), ins...), s[b:]...)
	case block == "":
		out = append(out, s...)
	case len(s) == 0:
		out = append(out, ins...)
	default:
		out = append(append(append(out, s...), units[E](nl)...), ins...)
	}
	return out, nil
}

var (
	bomUTF8    = []byte{0xef, 0xbb, 0xbf}
	bomUTF16LE = []byte{0xff, 0xfe}
	bomUTF16BE = []byte{0xfe, 0xff}
)

var errEncoding = errors.New("devtools: unsupported profile encoding (UTF-16BE or odd-length UTF-16)")

// editProfile applies f to the profile's units, keeping its BOM and
// encoding. f returns the new units; projection-only callers return nil.
func editProfile(data []byte, f func(u8 []byte, u16 []uint16) ([]byte, []uint16, error)) ([]byte, error) {
	switch {
	case hasPrefix(data, bomUTF16LE):
		body := data[2:]
		if len(body)%2 != 0 {
			return nil, errEncoding
		}
		u := make([]uint16, len(body)/2)
		for i := range u {
			u[i] = uint16(body[2*i]) | uint16(body[2*i+1])<<8
		}
		_, nu, err := f(nil, u)
		if err != nil || nu == nil {
			return nil, err
		}
		out := append([]byte(nil), bomUTF16LE...)
		for _, c := range nu {
			out = append(out, byte(c), byte(c>>8))
		}
		return out, nil
	case hasPrefix(data, bomUTF16BE):
		return nil, errEncoding
	default:
		var bom []byte
		if hasPrefix(data, bomUTF8) {
			bom, data = bomUTF8, data[3:]
		}
		nb, _, err := f(data, nil)
		if err != nil || nb == nil {
			return nil, err
		}
		return append(append([]byte(nil), bom...), nb...), nil
	}
}

func hasPrefix(b, p []byte) bool { return len(b) >= len(p) && string(b[:len(p)]) == string(p) }

// shellProjection is what the journal records for a profile: our block's
// text ("" when there is none) and whether the file exists.
type shellProjection struct {
	Block  string `json:"block"`
	Exists bool   `json:"exists"`
}

// shellCurrent is the projection of data; exists tells whether the file
// exists at all.
func shellCurrent(data []byte, exists bool) (json.RawMessage, error) {
	var block string
	_, err := editProfile(data, func(b []byte, u []uint16) ([]byte, []uint16, error) {
		var err error
		if u != nil {
			block, err = blockOf(u)
		} else {
			block, err = blockOf(b)
		}
		return nil, nil, err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(shellProjection{Block: block, Exists: exists})
}

// blockIsLast reports whether nothing but blank lines follows our block.
func blockIsLast(data []byte) bool {
	last := true
	editProfile(data, func(b []byte, u []uint16) ([]byte, []uint16, error) {
		if u != nil {
			last = tailBlank(u)
		} else {
			last = tailBlank(b)
		}
		return nil, nil, nil
	})
	return last
}

func tailBlank[E unit](s []E) bool {
	_, end, found, err := findBlock(s)
	if err != nil || !found {
		return true
	}
	for _, c := range s[end:] {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return false
		}
	}
	return true
}

// shellSet returns data with our block set to block ("" removes it).
func shellSet(data []byte, block, defNL string) ([]byte, error) {
	out, err := editProfile(data, func(b []byte, u []uint16) ([]byte, []uint16, error) {
		if u != nil {
			nu, err := withBlock(u, block, defNL)
			return nil, nu, err
		}
		nb, err := withBlock(b, block, defNL)
		return nb, nil, err
	})
	if err == nil && out == nil {
		out = []byte{}
	}
	return out, err
}
