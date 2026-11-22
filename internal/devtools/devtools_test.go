package devtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/tailscale/hujson"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	fixtures  = "../../testdata/devtools/"
	testPort  = 35201
	testProxy = "http://127.0.0.1:35201"
)

type env struct {
	t     *testing.T
	roots Roots
	j     *journal.Journal
	fs    *osFS
	m     *Manager
}

// newEnv builds temp roots; dirs are created relative to the temp base
// ("home/.claude", "docs/PowerShell", ...).
func newEnv(t *testing.T, dirs ...string) *env {
	t.Helper()
	base := t.TempDir()
	r := Roots{
		Home:      filepath.Join(base, "home"),
		Documents: filepath.Join(base, "docs"),
		AppData:   filepath.Join(base, "appdata"),
		StateFile: filepath.Join(base, "zatvor", "run", "devtools.proxy"),
		BinDir:    filepath.Join(base, "zatvor", "bin"),
	}
	for _, d := range append([]string{"home", "docs", "appdata", "zatvor/run"}, dirs...) {
		if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	j, err := journal.Open(filepath.Join(base, "zatvor", "journal"))
	if err != nil {
		t.Fatal(err)
	}
	fs := &osFS{}
	return &env{t: t, roots: r, j: j, fs: fs, m: New(r, j, fs)}
}

func (e *env) target(name string) Target {
	e.t.Helper()
	for _, tg := range e.m.Status() {
		if tg.Name == name {
			return tg
		}
	}
	e.t.Fatalf("no target %q in Status", name)
	return Target{}
}

func (e *env) path(name string) string { return e.target(name).Path }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func put(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustEnable(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Enable(testPort); err != nil {
		t.Fatalf("Enable: %v", err)
	}
}

func mustDisable(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
}

func decodeUTF16LE(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) < 2 || b[0] != 0xff || b[1] != 0xfe || len(b)%2 != 0 {
		t.Fatalf("not UTF-16LE with BOM: % x", b[:min(len(b), 4)])
	}
	u := make([]uint16, 0, len(b)/2-1)
	for i := 2; i < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// onlyCRLF reports whether every LF in s is part of a CRLF.
func onlyCRLF(s string) bool { return strings.Count(s, "\n") == strings.Count(s, "\r\n") }

func TestBlockInsertedOnceAndRemovedExactly(t *testing.T) {
	e := newEnv(t, "docs/WindowsPowerShell", "docs/PowerShell")
	ps5, ps7, bash := e.path("pwsh5"), e.path("pwsh7"), e.path("bash")
	origPS5 := fixture(t, "profile_crlf_bom.ps1")
	origPS7 := fixture(t, "profile_utf16le.ps1")
	origBash := fixture(t, "bashrc")
	put(t, ps5, origPS5)
	put(t, ps7, origPS7)
	put(t, bash, origBash)

	mustEnable(t, e.m)
	mustEnable(t, e.m)

	got5 := string(read(t, ps5))
	if strings.Count(got5, beginMarker) != 1 || strings.Count(got5, endMarker) != 1 {
		t.Fatalf("pwsh5: want one block after two Enables, got:\n%s", got5)
	}
	if !strings.HasPrefix(got5, string(origPS5)) {
		t.Errorf("pwsh5: user content (BOM included) not kept as the prefix")
	}
	if !onlyCRLF(got5) {
		t.Errorf("pwsh5: block does not use the file's CRLF newlines")
	}
	got7 := decodeUTF16LE(t, read(t, ps7))
	if strings.Count(got7, beginMarker) != 1 || !onlyCRLF(got7) {
		t.Errorf("pwsh7 (UTF-16LE): want one CRLF block, got:\n%s", got7)
	}
	gotBash := string(read(t, bash))
	if strings.Count(gotBash, beginMarker) != 1 || strings.Contains(gotBash, "\r") {
		t.Errorf("bash: want one LF block, got:\n%q", gotBash)
	}
	for _, n := range []string{"pwsh5", "pwsh7", "bash"} {
		if s := e.target(n).State; s != On {
			t.Errorf("%s: state %q, want on", n, s)
		}
	}

	mustDisable(t, e.m)
	for _, c := range []struct {
		path string
		want []byte
	}{{ps5, origPS5}, {ps7, origPS7}, {bash, origBash}} {
		if got := read(t, c.path); !bytes.Equal(got, c.want) {
			t.Errorf("%s not restored byte-equal:\n got %q\nwant %q", filepath.Base(c.path), got, c.want)
		}
	}
	if s := e.target("pwsh5").State; s != Off {
		t.Errorf("pwsh5 after Disable: %q, want off", s)
	}
	if p, _ := e.j.Pending(); len(p) != 0 {
		t.Errorf("journal still has %d pending entries", len(p))
	}
	if _, err := os.Stat(ps5 + ".zatvor.bak"); err != nil {
		t.Errorf("no one-time backup: %v", err)
	} else if b := read(t, ps5+".zatvor.bak"); !bytes.Equal(b, origPS5) {
		t.Errorf("backup is not the original file")
	}
}

func TestMissingAndNewFiles(t *testing.T) {
	// Documents\PowerShell exists only with pwsh 7 installed: never created.
	e := newEnv(t, "docs/WindowsPowerShell")
	mustEnable(t, e.m)

	if tg := e.target("pwsh7"); tg.State != Missing {
		t.Errorf("pwsh7 without its directory: %q, want missing", tg.State)
	}
	if _, err := os.Stat(filepath.Join(e.roots.Documents, "PowerShell")); !os.IsNotExist(err) {
		t.Errorf("pwsh7 parent directory was created: %v", err)
	}
	for _, n := range []string{"claude", "vscode"} {
		if tg := e.target(n); tg.State != Missing {
			t.Errorf("%s without its directory: %q, want missing", n, tg.State)
		}
	}
	ps5 := read(t, e.path("pwsh5"))
	if bytes.HasPrefix(ps5, []byte{0xef, 0xbb, 0xbf}) || !onlyCRLF(string(ps5)) || !bytes.HasPrefix(ps5, []byte(beginMarker+"\r\n")) {
		t.Errorf("new .ps1 profile: want CRLF, no BOM, block first; got %q", ps5)
	}
	bashrc := read(t, e.path("bash"))
	if bytes.Contains(bashrc, []byte("\r")) || !bytes.HasPrefix(bashrc, []byte(beginMarker+"\n")) {
		t.Errorf("new .bashrc: want LF; got %q", bashrc)
	}
	mustDisable(t, e.m)
	if b := read(t, e.path("pwsh5")); len(b) != 0 {
		t.Errorf("created profile after Disable: %q, want empty", b)
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" && isPrivilegeError(err) {
			t.Skip("creating a symlink on this Windows runner needs the SeCreateSymbolicLink privilege (Developer Mode off)")
		}
		t.Fatal(err)
	}
}

func TestWritesThroughSymlink(t *testing.T) {
	e := newEnv(t, "home/.claude", "dotfiles")
	dot := filepath.Join(filepath.Dir(e.roots.Home), "dotfiles")
	realBash := filepath.Join(dot, "bashrc")
	realClaude := filepath.Join(dot, "claude.json")
	origBash, origClaude := fixture(t, "bashrc"), fixture(t, "claude_settings.json")
	if err := os.WriteFile(realBash, origBash, 0o640); err != nil {
		t.Fatal(err)
	}
	put(t, realClaude, origClaude)
	symlinkOrSkip(t, realBash, e.path("bash"))
	symlinkOrSkip(t, realClaude, e.path("claude"))

	mustEnable(t, e.m)
	for _, c := range []struct{ link, target string }{{e.path("bash"), realBash}, {e.path("claude"), realClaude}} {
		fi, err := os.Lstat(c.link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a symlink (%v)", c.link, err)
		}
		if dst, _ := os.Readlink(c.link); dst != c.target {
			t.Errorf("link now points at %q, want %q", dst, c.target)
		}
		if _, err := os.Stat(c.target + ".zatvor.bak"); err != nil {
			t.Errorf("backup not next to the link target: %v", err)
		}
	}
	if !strings.Contains(string(read(t, realBash)), beginMarker) {
		t.Errorf("link target did not get the block")
	}
	if !strings.Contains(string(read(t, realClaude)), testProxy) {
		t.Errorf("link target did not get the key")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(realBash); fi.Mode().Perm() != 0o640 {
			t.Errorf("mode changed to %v, want 0640", fi.Mode().Perm())
		}
	}

	mustDisable(t, e.m)
	if fi, err := os.Lstat(e.path("bash")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced on Disable (%v)", err)
	}
	if got := read(t, realBash); !bytes.Equal(got, origBash) {
		t.Errorf("bashrc target not restored:\n%q", got)
	}
	if got := read(t, realClaude); !bytes.Equal(got, origClaude) {
		t.Errorf("claude target not restored:\n%s", got)
	}
}

func TestJSONCCommentsKept(t *testing.T) {
	e := newEnv(t, "appdata/Code/User")
	path := e.path("vscode")
	orig := fixture(t, "vscode_settings.jsonc")
	put(t, path, orig)

	mustEnable(t, e.m)
	got := read(t, path)
	for _, keep := range []string{"// VS Code user settings", "// Editor", "// big enough", "/* Files */",
		`"files.eol": "\r\n",`, `"workbench.colorTheme": "Default Dark+",`, `"editor.fontSize": 14,`} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
	if !onlyCRLF(string(got)) {
		t.Errorf("inserted members do not use CRLF:\n%q", got)
	}
	v, err := hujson.Parse(got)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, got)
	}
	for ptr, want := range map[string]string{"/http.proxy": `"` + testProxy + `"`, "/http.proxySupport": `"override"`} {
		if f := v.Find(ptr); f == nil || string(f.Value.(hujson.Literal)) != want {
			t.Errorf("%s = %v, want %s", ptr, f, want)
		}
	}
	mustDisable(t, e.m)
	if got := read(t, path); !bytes.Equal(got, orig) {
		t.Errorf("not restored byte-equal:\n got %q\nwant %q", got, orig)
	}

	// A user proxy is recorded, replaced in place and put back on Disable.
	user := fixture(t, "vscode_settings_userproxy.jsonc")
	put(t, path, user)
	mustEnable(t, e.m)
	got = read(t, path)
	if !bytes.HasPrefix(got, []byte{0xef, 0xbb, 0xbf}) {
		t.Errorf("BOM lost")
	}
	for _, keep := range []string{"// Corporate proxy, set by IT", "/* was fallback */", `"telemetry.telemetryLevel": "off"`, `"http.proxy": "` + testProxy + `"`, `"http.proxySupport": "override" /* was fallback */,`} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Errorf("want %q in:\n%s", keep, got)
		}
	}
	mustDisable(t, e.m)
	if got := read(t, path); !bytes.Equal(got, user) {
		t.Errorf("user proxy not restored byte-equal:\n got %q\nwant %q", got, user)
	}
}

func TestRaceRetryNeverClobbers(t *testing.T) {
	t.Run("retry merges", func(t *testing.T) {
		e := newEnv(t, "home/.claude")
		path := e.path("claude")
		put(t, path, fixture(t, "claude_settings.json"))
		raced := false
		e.fs.beforeRename = func(p string) {
			if raced || !strings.HasSuffix(p, "settings.json") {
				return
			}
			raced = true // the owning app saves a new model between our read and rename
			put(t, path, bytes.Replace(read(t, path), []byte(`"opus"`), []byte(`"sonnet"`), 1))
		}
		mustEnable(t, e.m)
		got := string(read(t, path))
		if !strings.Contains(got, `"model": "sonnet"`) || !strings.Contains(got, testProxy) {
			t.Fatalf("retry did not merge the other writer's change with ours:\n%s", got)
		}
		if p := pendingFor(t, e.j, path); len(p) != 1 {
			t.Errorf("want 1 pending entry for the file (the lost attempt closed), got %d", len(p))
		}
	})

	t.Run("three losses is a conflict", func(t *testing.T) {
		e := newEnv(t, "docs/WindowsPowerShell")
		path := e.path("pwsh5")
		put(t, path, fixture(t, "profile_crlf_bom.ps1"))
		var last []byte
		n := 0
		e.fs.beforeRename = func(p string) {
			if p != path {
				return
			}
			n++
			last = append(fixture(t, "profile_crlf_bom.ps1"), []byte("# edit "+string(rune('0'+n))+"\r\n")...)
			put(t, path, last)
		}
		err := e.m.Enable(testPort)
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeConflict || de.Target != "pwsh5" {
			t.Fatalf("Enable = %v, want a %s error for pwsh5", err, CodeConflict)
		}
		if n != maxAttempts {
			t.Errorf("attempts = %d, want %d", n, maxAttempts)
		}
		if got := read(t, path); !bytes.Equal(got, last) {
			t.Errorf("the other writer's bytes were not kept:\n got %q\nwant %q", got, last)
		}
		if tg := e.target("pwsh5"); tg.State != Conflict {
			t.Errorf("state %q, want conflict", tg.State)
		}
		if p := pendingFor(t, e.j, path); len(p) != 0 {
			t.Errorf("unwritten attempts left %d pending entries", len(p))
		}
		ents, _ := os.ReadDir(filepath.Dir(path))
		for _, de := range ents {
			if strings.Contains(de.Name(), ".tmp") {
				t.Errorf("temp file left behind: %s", de.Name())
			}
		}
	})
}

func pendingFor(t *testing.T, j *journal.Journal, path string) []journal.Entry {
	t.Helper()
	p, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.Entry
	for _, e := range p {
		if e.Kind == Kind && strings.HasSuffix(e.Target, ":"+path) {
			out = append(out, e)
		}
	}
	return out
}

func TestOnlyOurKeysReverted(t *testing.T) {
	e := newEnv(t, "home/.claude")
	path := e.path("claude")
	orig := fixture(t, "claude_settings.json")
	put(t, path, orig)
	mustEnable(t, e.m)

	// The user (or Claude Code) edits other keys while we are enabled.
	b := read(t, path)
	b = bytes.Replace(b, []byte(`"opus"`), []byte(`"sonnet"`), 1)
	b = bytes.Replace(b, []byte(`"FOO": "1"`), []byte(`"FOO": "2"`), 1)
	put(t, path, b)
	mustDisable(t, e.m)

	want := bytes.Replace(bytes.Replace(orig, []byte(`"opus"`), []byte(`"sonnet"`), 1), []byte(`"FOO": "1"`), []byte(`"FOO": "2"`), 1)
	if got := read(t, path); !bytes.Equal(got, want) {
		t.Errorf("Disable did not keep the user's edits and drop only ours:\n got %s\nwant %s", got, want)
	}
	if !json.Valid(read(t, path)) {
		t.Errorf("result is not strict JSON")
	}

	// A user's own HTTPS_PROXY is replaced on Enable and comes back on Disable.
	userProxy := []byte("{\n  \"env\": {\n    \"HTTPS_PROXY\": \"http://user.example:8080\"\n  }\n}\n")
	put(t, path, userProxy)
	mustEnable(t, e.m)
	if got := read(t, path); !bytes.Contains(got, []byte(testProxy)) {
		t.Fatalf("our proxy not set:\n%s", got)
	}
	mustDisable(t, e.m)
	if got := read(t, path); !bytes.Equal(got, userProxy) {
		t.Errorf("user's HTTPS_PROXY not restored:\n%s", got)
	}

	// Someone changes our key while we are enabled: theirs is kept.
	put(t, path, orig)
	mustEnable(t, e.m)
	theirs := bytes.Replace(read(t, path), []byte(testProxy), []byte("http://other.example:1"), 1)
	put(t, path, theirs)
	if tg := e.target("claude"); tg.State != Conflict {
		t.Errorf("state after a foreign change of our key: %q, want conflict", tg.State)
	}
	mustDisable(t, e.m)
	if got := read(t, path); !bytes.Equal(got, theirs) {
		t.Errorf("a value changed by someone else was reverted:\n%s", got)
	}
	var skipped bool
	for _, r := range e.m.LastRevert() {
		skipped = skipped || r.Outcome == journal.SkippedChangedByOther
	}
	if !skipped {
		t.Errorf("LastRevert does not report the skipped entry: %+v", e.m.LastRevert())
	}
}

func TestStateFileSwitch(t *testing.T) {
	ps := pwshBlock(`C:\Users\test\AppData\Local\Zatvor\Run\devtools.proxy`)
	sh := bashBlock(`C:\Users\test\AppData\Local\Zatvor\Run\devtools.proxy`)
	golden(t, "pwsh_block.ps1", ps)
	golden(t, "bash_block.sh", sh)
	for _, want := range []string{"GetLastWriteTimeUtc", "__zatvorPrompt", "localhost,127.0.0.1,::1"} {
		if !strings.Contains(ps, want) {
			t.Errorf("PowerShell block lacks %q", want)
		}
	}
	if !strings.Contains(sh, "/c/Users/test/AppData/Local/Zatvor/Run/devtools.proxy") {
		t.Errorf("bash block does not use the Git Bash form of a drive path")
	}
	if q := pwshBlock(`C:\it's\state`); !strings.Contains(q, `'C:\it''s\state'`) {
		t.Errorf("PowerShell quote not doubled")
	}
	if q := bashBlock(`/tmp/it's/state`); !strings.Contains(q, `'/tmp/it'\''s/state'`) {
		t.Errorf("bash quote not escaped")
	}

	t.Run("bash switches at the next prompt", func(t *testing.T) {
		bashPath, err := exec.LookPath("bash")
		if err != nil || runtime.GOOS == "windows" {
			t.Skip("needs a POSIX bash")
		}
		state := filepath.Join(t.TempDir(), "state")
		put(t, state, []byte(testProxy))
		script := "PROMPT_COMMAND='echo user'\n" + bashBlock(state) + `
echo "1:$HTTPS_PROXY|$HTTP_PROXY|$NO_PROXY"
printf '' > "$1"; __zatvor_sync
echo "2:${HTTPS_PROXY-unset}|${NO_PROXY-unset}"
export HTTPS_PROXY=http://mine.example:1; printf 'http://127.0.0.1:1' > "$1"; __zatvor_sync; printf '' > "$1"; __zatvor_sync
echo "3:${HTTPS_PROXY-unset}"
echo "4:$PROMPT_COMMAND"
`
		out, err := exec.Command(bashPath, "--norc", "--noprofile", "-c", script, "bash", state).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		want := "1:" + testProxy + "|" + testProxy + "|localhost,127.0.0.1,::1\n2:unset|unset\n3:unset\n4:echo user\n__zatvor_sync\n"
		if string(out) != want {
			t.Errorf("bash block output:\n%s\nwant:\n%s", out, want)
		}
	})

	t.Run("PowerShell switches at the next prompt", func(t *testing.T) {
		shell := ""
		for _, n := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(n); err == nil {
				shell = p
				break
			}
		}
		if shell == "" {
			t.Skip("no PowerShell on this machine")
		}
		dir := t.TempDir()
		state := filepath.Join(dir, "state")
		put(t, state, []byte(testProxy))
		script := filepath.Join(dir, "t.ps1")
		body := "function prompt { 'user> ' }\r\n" + strings.ReplaceAll(pwshBlock(state), "\n", "\r\n") +
			"$p = prompt\r\n\"1:$p|$env:HTTPS_PROXY|$env:NO_PROXY\"\r\n" +
			"Set-Content -LiteralPath '" + state + "' -Value '' -NoNewline\r\n" +
			"[IO.File]::SetLastWriteTimeUtc('" + state + "', [DateTime]::UtcNow.AddMinutes(1))\r\n" +
			"$null = prompt\r\n\"2:$([string]::IsNullOrEmpty($env:HTTPS_PROXY))\"\r\n"
		put(t, script, []byte(body))
		out, err := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		want := "1:user> |" + testProxy + "|localhost,127.0.0.1,::1\n2:True\n"
		if got := strings.ReplaceAll(string(out), "\r\n", "\n"); got != want {
			t.Errorf("PowerShell block output:\n%s\nwant:\n%s", got, want)
		}
	})
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := fixtures + "golden/" + name
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from golden:\n%s", name, got)
	}
}

func TestCodexRespectSystemProxyNote(t *testing.T) {
	e := newEnv(t, "home/.codex")
	cfg := filepath.Join(e.roots.Home, ".codex", "config.toml")
	for _, c := range []struct {
		body string
		note bool
	}{
		{"model = \"o3\"\nrespect_system_proxy = true # use the OS proxy\n", true},
		{"respect_system_proxy=true\n", true},
		{"respect_system_proxy = false\n", false},
		{"# respect_system_proxy = true\n", false},
		{"", false},
	} {
		put(t, cfg, []byte(c.body))
		note := e.target("codex").Note
		if got := strings.Contains(note, "respect_system_proxy"); got != c.note {
			t.Errorf("config %q: note %q, want note=%v", c.body, note, c.note)
		}
		if e.target("gemini").Note != "" && strings.Contains(e.target("gemini").Note, "respect_system_proxy") {
			t.Errorf("gemini got the codex note")
		}
	}
	put(t, cfg, []byte("respect_system_proxy = true\n"))
	mustEnable(t, e.m)
	mustDisable(t, e.m)
	if got := read(t, cfg); string(got) != "respect_system_proxy = true\n" {
		t.Errorf("config.toml was edited: %q", got)
	}
}

func TestWrapperFindsRealToolNotItself(t *testing.T) {
	e := newEnv(t)
	put(t, e.roots.StateFile, []byte(testProxy))
	mustEnable(t, e.m)
	tg := e.target("codex")
	if tg.Path != e.roots.BinDir || tg.State != On {
		t.Fatalf("codex target = %+v, want On at BinDir", tg)
	}
	for _, n := range []string{"codex.cmd", "codex.ps1", "codex", "gemini.cmd", "gemini.ps1", "gemini"} {
		if _, err := os.Stat(filepath.Join(e.roots.BinDir, n)); err != nil {
			t.Errorf("wrapper %s: %v", n, err)
		}
	}

	realDir := t.TempDir()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		put(t, filepath.Join(realDir, "codex.cmd"), []byte("@echo off\r\necho real:%HTTPS_PROXY%:%NO_PROXY%:%*\r\n"))
		cmd = exec.Command("cmd.exe", "/d", "/c", filepath.Join(e.roots.BinDir, "codex.cmd"), "exec", "hi")
	} else {
		if err := os.WriteFile(filepath.Join(realDir, "codex"), []byte("#!/bin/sh\necho \"real:$HTTPS_PROXY:$NO_PROXY:$*\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(filepath.Join(e.roots.BinDir, "codex"), "exec", "hi")
	}
	sep := string(os.PathListSeparator)
	// BinDir first, as the user would put it, once more with a trailing separator.
	cmd.Env = append(os.Environ(), "PATH="+e.roots.BinDir+sep+e.roots.BinDir+string(filepath.Separator)+sep+realDir+sep+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := "real:" + testProxy + ":localhost,127.0.0.1,::1:exec hi"
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("wrapper ran %q, want %q", got, want)
	}

	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("powershell"); err == nil {
			c := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(e.roots.BinDir, "codex.ps1"), "exec", "hi")
			c.Env = cmd.Env
			out, err := c.CombinedOutput()
			if err != nil {
				t.Fatalf("codex.ps1: %v: %s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("codex.ps1 ran %q, want %q", got, want)
			}
		}
	}

	// Only the wrapper on PATH: it fails instead of calling itself.
	cmd2 := exec.Command(cmd.Args[0], cmd.Args[1:]...)
	cmd2.Env = append(os.Environ(), "PATH="+e.roots.BinDir+sep+filepath.Dir(cmd.Path))
	if out, err := cmd2.CombinedOutput(); err == nil || !strings.Contains(string(out), "not found") {
		t.Errorf("wrapper without a real tool: err=%v out=%s", err, out)
	}

	mustDisable(t, e.m)
	if ents, _ := os.ReadDir(e.roots.BinDir); len(ents) != 0 {
		t.Errorf("Disable left wrappers: %v", ents)
	}
	if tg := e.target("codex"); tg.State != Off {
		t.Errorf("codex after Disable: %q", tg.State)
	}
}

// journalCheckFS asserts that a write of a user file is preceded by a pending
// journal entry whose Applied is the projection of the bytes being written.
type journalCheckFS struct {
	FS
	t       *testing.T
	j       *journal.Journal
	checked int
}

func (f *journalCheckFS) WriteFile(path string, data []byte, w WriteOpts) error {
	if w.Backup { // user files only; wrappers are ours
		f.checked++
		p := pendingFor(f.t, f.j, path)
		if len(p) == 0 {
			f.t.Errorf("write of %s without a pending journal entry", path)
		} else {
			last := p[len(p)-1]
			tg, _, _ := strings.Cut(last.Target, ":")
			proj, err := projectionOf(tg, data)
			if err != nil || !bytes.Equal(canonJSON(proj), canonJSON(last.Applied)) {
				f.t.Errorf("journaled Applied %s does not match the written bytes (%s, %v)", last.Applied, proj, err)
			}
		}
	}
	return f.FS.WriteFile(path, data, w)
}

func canonJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	c, _ := json.Marshal(v)
	return c
}

func TestJournaledBeforeWrite(t *testing.T) {
	e := newEnv(t, "docs/WindowsPowerShell", "home/.claude", "appdata/Code/User")
	cfs := &journalCheckFS{FS: e.fs, t: t, j: e.j}
	m := New(e.roots, e.j, cfs)
	put(t, filepath.Join(e.roots.AppData, "Code", "User", "settings.json"), fixture(t, "vscode_settings.jsonc"))
	mustEnable(t, m)
	if cfs.checked != 4 { // pwsh5, bash, claude, vscode
		t.Errorf("checked %d user-file writes, want 4", cfs.checked)
	}
	p, _ := e.j.Pending()
	for _, en := range p {
		if en.Kind != Kind {
			t.Errorf("entry kind %q", en.Kind)
		}
		name, path, _ := strings.Cut(en.Target, ":")
		if !filepath.IsAbs(path) || name == "" {
			t.Errorf("target %q is not <name>:<absolute path>", en.Target)
		}
	}
	if len(p) != 4 {
		t.Errorf("pending entries = %d, want 4 (one per file)", len(p))
	}
}

func TestRevertAfterCrash(t *testing.T) {
	e := newEnv(t, "docs/WindowsPowerShell", "appdata/Code/User")
	orig := fixture(t, "vscode_settings.jsonc")
	vs := e.path("vscode")
	put(t, vs, orig)
	mustEnable(t, e.m)

	// The daemon dies; the next start reverts from the journal alone.
	m2 := New(e.roots, e.j, &osFS{})
	for _, r := range e.j.RevertAll(map[string]journal.Reverter{Kind: m2.Reverter()}) {
		if r.Outcome != journal.Restored {
			t.Errorf("%s: %s %v", r.Target, r.Outcome, r.Err)
		}
	}
	if got := read(t, vs); !bytes.Equal(got, orig) {
		t.Errorf("vscode settings not restored:\n%q", got)
	}
	if b := read(t, e.path("pwsh5")); len(b) != 0 {
		t.Errorf("pwsh5 block not removed: %q", b)
	}
}

func TestRecorderChangesNothing(t *testing.T) {
	e := newEnv(t, "home/.claude")
	claude := e.path("claude")
	orig := fixture(t, "claude_settings.json")
	put(t, claude, orig)
	var log bytes.Buffer
	m := New(e.roots, e.j, NewRecorder(&log))
	mustEnable(t, m)
	if got := read(t, claude); !bytes.Equal(got, orig) {
		t.Errorf("recorder changed the file")
	}
	if _, err := os.Stat(e.roots.BinDir); !os.IsNotExist(err) {
		t.Errorf("recorder created BinDir: %v", err)
	}
	if _, err := os.Stat(e.path("bash")); !os.IsNotExist(err) {
		t.Errorf("recorder created .bashrc")
	}
	if s := statusOf(m, "claude"); s != On {
		t.Errorf("recorded state %q, want on", s)
	}
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	var writes int
	for _, l := range lines {
		var rec struct {
			Op   string `json:"op"`
			Args struct {
				Path string `json:"path"`
			} `json:"args"`
		}
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		if rec.Op == "file.write" {
			writes++
		}
		if strings.Contains(l, `"FOO"`) || strings.Contains(l, "opus") {
			t.Errorf("recorder logged file content: %s", l)
		}
	}
	if writes == 0 {
		t.Errorf("no file.write recorded:\n%s", log.String())
	}
	mustDisable(t, m)
	if got := read(t, claude); !bytes.Equal(got, orig) {
		t.Errorf("recorder changed the file on Disable")
	}
	if s := statusOf(m, "claude"); s != Off {
		t.Errorf("recorded state after Disable %q, want off", s)
	}
}

func statusOf(m *Manager, name string) State {
	for _, tg := range m.Status() {
		if tg.Name == name {
			return tg.State
		}
	}
	return ""
}

// Windows PowerShell 5.1 reads BOM-less scripts as ANSI and cmd.exe reads
// batch files as OEM, so a Cyrillic user name must not appear literally.
func TestNonASCIIPathsStayASCII(t *testing.T) {
	base := filepath.Join(string(filepath.Separator)+"Users", "Артур", "AppData", "Local", "Zatvor")
	state := filepath.Join(base, "run", "devtools.proxy")
	bin := filepath.Join(base, "bin")
	if b := pwshBlock(state); !isASCII(b) || !strings.Contains(b, "FromBase64String") {
		t.Errorf("PowerShell block is not ASCII or does not decode the path:\n%s", b)
	}
	for _, f := range wrapperFiles("codex", bin, state) {
		if f.name == "codex" {
			continue // POSIX shells read UTF-8
		}
		if !isASCII(f.body) {
			t.Errorf("%s is not ASCII", f.name)
		}
	}
	if !strings.Contains(cmdWrapper("codex", bin, state), `%~dp0..\run\devtools.proxy`) {
		t.Errorf("cmd wrapper does not find the state file relative to itself")
	}
}

func TestUnsetRootIsMissing(t *testing.T) {
	e := newEnv(t)
	r := e.roots
	r.Documents, r.AppData = "", ""
	m := New(r, e.j, e.fs)
	mustEnable(t, m)
	for _, n := range []string{"pwsh5", "pwsh7", "vscode"} {
		if s := statusOf(m, n); s != Missing {
			t.Errorf("%s with an unset root: %q, want missing", n, s)
		}
	}
	if ents, _ := os.ReadDir("."); len(ents) == 0 {
		t.Fatal("cannot list the working directory")
	}
	for _, d := range []string{"WindowsPowerShell", "PowerShell", "Code"} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("created %s relative to the working directory", d)
		}
	}
}
