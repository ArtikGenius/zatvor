package safemode

import "testing"

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveDevDefaultsSafe(t *testing.T) {
	if releaseBuild {
		t.Skip("dev-build default; this binary was built with -tags release")
	}
	mode, src := Resolve(nil, envOf(nil))
	if mode != Safe || src != SourceBuildDefault {
		t.Fatalf("got %v (%v), want safe (build default)", mode, src)
	}
	if got := mode.String() + " (" + src.String() + ")"; got != "safe (build default)" {
		t.Fatalf("text = %q", got)
	}
}

func TestResolveLiveRefusedWithoutAllow(t *testing.T) {
	if releaseBuild {
		t.Skip("the allow gate applies to dev builds only")
	}
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"flag", []string{"--live"}, nil},
		{"single-dash flag", []string{"-live"}, nil},
		{"env", nil, map[string]string{"ZATVOR_SAFE": "0"}},
		{"allow not exactly 1", []string{"--live"}, map[string]string{"ZATVOR_ALLOW_LIVE": "true"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, src := Resolve(c.args, envOf(c.env))
			if mode != Safe || src != SourceLiveRefused {
				t.Fatalf("got %v (%v), want safe (live refused)", mode, src)
			}
		})
	}
	// With the allow switch the same request goes through.
	mode, src := Resolve([]string{"--live"}, envOf(map[string]string{"ZATVOR_ALLOW_LIVE": "1"}))
	if mode != Live || src != SourceFlag {
		t.Fatalf("allowed: got %v (%v), want live (flag)", mode, src)
	}
}

func TestResolveEnvOverridesDefault(t *testing.T) {
	allow := map[string]string{"ZATVOR_SAFE": "0", "ZATVOR_ALLOW_LIVE": "1"}
	if mode, src := Resolve(nil, envOf(allow)); mode != Live || src != SourceEnv {
		t.Fatalf("ZATVOR_SAFE=0: got %v (%v), want live (env)", mode, src)
	}
	if mode, src := Resolve(nil, envOf(map[string]string{"ZATVOR_SAFE": "1"})); mode != Safe || src != SourceEnv {
		t.Fatalf("ZATVOR_SAFE=1: got %v (%v), want safe (env)", mode, src)
	}
	// Any other value is doubt: safe.
	if mode, src := Resolve(nil, envOf(map[string]string{"ZATVOR_SAFE": "no", "ZATVOR_ALLOW_LIVE": "1"})); mode != Safe || src != SourceEnv {
		t.Fatalf("ZATVOR_SAFE=no: got %v (%v), want safe (env)", mode, src)
	}
}

func TestResolveFlagWinsOverEnv(t *testing.T) {
	env := envOf(map[string]string{"ZATVOR_SAFE": "0", "ZATVOR_ALLOW_LIVE": "1"})
	if mode, src := Resolve([]string{"--safe"}, env); mode != Safe || src != SourceFlag {
		t.Fatalf("--safe over ZATVOR_SAFE=0: got %v (%v), want safe (flag)", mode, src)
	}
	env = envOf(map[string]string{"ZATVOR_SAFE": "1", "ZATVOR_ALLOW_LIVE": "1"})
	if mode, src := Resolve([]string{"--live"}, env); mode != Live || src != SourceFlag {
		t.Fatalf("--live over ZATVOR_SAFE=1: got %v (%v), want live (flag)", mode, src)
	}
	// Both flags at once: safe wins.
	if mode, src := Resolve([]string{"--live", "--safe"}, env); mode != Safe || src != SourceFlag {
		t.Fatalf("--live --safe: got %v (%v), want safe (flag)", mode, src)
	}
	// Flags after "--" are not flags.
	if mode, src := Resolve([]string{"--", "--live"}, envOf(map[string]string{"ZATVOR_ALLOW_LIVE": "1"})); mode != buildDefault || src != SourceBuildDefault {
		t.Fatalf("after --: got %v (%v)", mode, src)
	}
}
