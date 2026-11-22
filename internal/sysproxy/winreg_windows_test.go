package sysproxy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The structs must match the C layout WinINet and RAS read.
func TestWinStructLayout(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	var o perConnOption
	var l perConnOptionList
	if unsafe.Sizeof(o) != 8+ptr || unsafe.Offsetof(o.Value) != ptr {
		t.Fatalf("INTERNET_PER_CONN_OPTIONW: size %d, Value at %d", unsafe.Sizeof(o), unsafe.Offsetof(o.Value))
	}
	want := map[uintptr][2]uintptr{4: {20, 16}, 8: {32, 24}}[ptr] // size, pOptions offset
	if unsafe.Sizeof(l) != want[0] || unsafe.Offsetof(l.Options) != want[1] {
		t.Fatalf("INTERNET_PER_CONN_OPTION_LISTW: size %d, pOptions at %d; want %v", unsafe.Sizeof(l), unsafe.Offsetof(l.Options), want)
	}
	if n := unsafe.Sizeof(rasEntryName{}); n != 1048 {
		t.Fatalf("RASENTRYNAMEW size %d, want 1048", n)
	}
}

func TestSplitBypassRoundTrip(t *testing.T) {
	for _, v := range []string{"", "<local>", "a;b", "z;<local>;a;", ";x; spaced ;"} {
		parts := splitBypass(v)
		if got := strings.Join(parts, ";"); got != v {
			t.Fatalf("join(split(%q)) = %q", v, got)
		}
	}
	if !slices.Equal(splitBypass("b;a"), []string{"b", "a"}) {
		t.Fatal("order not kept")
	}
}

// Item 11: only ERROR_INVALID_PARAMETER (WinINet without FLAGS_UI) falls
// back to FLAGS; any other error is returned as it is.
func TestFlagsUIFallback(t *testing.T) {
	invalid := fmt.Errorf("InternetSetOptionW: %w", windows.ERROR_INVALID_PARAMETER)
	denied := fmt.Errorf("InternetSetOptionW: %w", windows.ERROR_ACCESS_DENIED)
	other := errors.New("fallback failed")
	for _, tc := range []struct {
		name          string
		first, second error
		want          error
		fellBack      bool
	}{
		{"ok", nil, nil, nil, false},
		{"invalid parameter", invalid, nil, nil, true},
		{"invalid parameter, then an error", invalid, other, other, true},
		{"access denied", denied, nil, denied, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fell := false
			err := flagsUIFallback(func() error { return tc.first }, func() error { fell = true; return tc.second })
			if err != tc.want || fell != tc.fellBack {
				t.Fatalf("err = %v, fell back = %v; want %v, %v", err, fell, tc.want, tc.fellBack)
			}
		})
	}
}
