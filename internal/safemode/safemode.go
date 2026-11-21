// Package safemode decides whether a Zatvor binary may change the system.
//
// In safe mode nothing on the host is touched: system calls go to the
// recorder in package sysapi instead. Dev builds (no "release" build tag)
// default to safe and refuse live mode unless ZATVOR_ALLOW_LIVE=1 is also set
// (only the CI systemtest job sets it). Any doubt resolves to safe.
package safemode

// Mode is safe or live.
type Mode int

const (
	Safe Mode = iota
	Live
)

func (m Mode) String() string {
	if m == Live {
		return "live"
	}
	return "safe"
}

// Source says where the mode came from.
type Source int

const (
	SourceBuildDefault Source = iota
	SourceEnv
	SourceFlag
	// SourceLiveRefused: live was asked for but this build needs
	// ZATVOR_ALLOW_LIVE=1, which was not set; the mode is safe.
	SourceLiveRefused
)

func (s Source) String() string {
	switch s {
	case SourceEnv:
		return "env ZATVOR_SAFE"
	case SourceFlag:
		return "flag"
	case SourceLiveRefused:
		return "live refused: ZATVOR_ALLOW_LIVE=1 not set"
	default:
		return "build default"
	}
}

// Resolve picks the mode. Precedence: flag (--safe/--live), then env
// ZATVOR_SAFE (1 = safe, 0 = live, anything else = safe), then the build
// default. --safe together with --live is safe. Args after "--" are not
// flags. In dev builds a live result needs env ZATVOR_ALLOW_LIVE=1 exactly,
// otherwise the result is (Safe, SourceLiveRefused).
func Resolve(args []string, env func(string) string) (Mode, Source) {
	if env == nil {
		env = func(string) string { return "" }
	}
	mode, src := buildDefault, SourceBuildDefault

	var sawSafe, sawLive bool
	for _, a := range args {
		if a == "--" {
			break
		}
		switch a {
		case "--safe", "-safe":
			sawSafe = true
		case "--live", "-live":
			sawLive = true
		}
	}
	switch {
	case sawSafe:
		mode, src = Safe, SourceFlag
	case sawLive:
		mode, src = Live, SourceFlag
	default:
		if v, ok := lookup(env, "ZATVOR_SAFE"); ok {
			src = SourceEnv
			if v == "0" {
				mode = Live
			} else {
				mode = Safe
			}
		}
	}

	if mode == Live && liveNeedsAllow && env("ZATVOR_ALLOW_LIVE") != "1" {
		return Safe, SourceLiveRefused
	}
	return mode, src
}

// lookup treats an empty value as unset, since env funcs such as os.Getenv
// cannot tell the two apart.
func lookup(env func(string) string, key string) (string, bool) {
	v := env(key)
	return v, v != ""
}
