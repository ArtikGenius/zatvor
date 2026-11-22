package sysproxy

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// The live system proxy goes through WinINet's per-connection options,
// which keep the HKCU Internet Settings values in step and tell running
// programs. We never write the registry ourselves, and never run netsh or
// reg.exe.

var (
	wininet                  = windows.NewLazySystemDLL("wininet.dll")
	procInternetQueryOptionW = wininet.NewProc("InternetQueryOptionW")
	procInternetSetOptionW   = wininet.NewProc("InternetSetOptionW")
	rasapi32                 = windows.NewLazySystemDLL("rasapi32.dll")
	procRasEnumEntriesW      = rasapi32.NewProc("RasEnumEntriesW")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalFree           = kernel32.NewProc("GlobalFree")
)

const (
	internetOptionRefresh             = 37
	internetOptionSettingsChanged     = 39
	internetOptionPerConnectionOption = 75

	perConnFlags         = 1
	perConnProxyServer   = 2
	perConnProxyBypass   = 3
	perConnAutoconfigURL = 4
	perConnFlagsUI       = 10 // IE8+: the flags as the settings UI shows them

	proxyTypeDirect       = 0x1
	proxyTypeProxy        = 0x2
	proxyTypeAutoProxyURL = 0x4
	proxyTypeAutoDetect   = 0x8

	errorBufferTooSmall = 603 // RAS error codes are returned, not set as last error
	rasMaxEntryName     = 256
	maxPath             = 260
)

// perConnOption is INTERNET_PER_CONN_OPTIONW. Value is the union of DWORD,
// LPWSTR and FILETIME: 8 bytes, at offset 8 on 64-bit (pointer alignment)
// and offset 4 on 386/arm, where Go also aligns uint64 to 4.
type perConnOption struct {
	Option uint32
	Value  uint64
}

// perConnOptionList is INTERNET_PER_CONN_OPTION_LISTW.
type perConnOptionList struct {
	Size        uint32
	Connection  *uint16 // nil: the LAN settings
	OptionCount uint32
	OptionError uint32
	Options     *perConnOption
}

// rasEntryName is RASENTRYNAMEW (WINVER >= 0x500).
type rasEntryName struct {
	Size          uint32
	EntryName     [rasMaxEntryName + 1]uint16
	Flags         uint32
	PhonebookPath [maxPath + 1]uint16
}

// str views the union as LPWSTR.
func (o *perConnOption) str() **uint16 { return (**uint16)(unsafe.Pointer(&o.Value)) }

type windowsProxy struct{}

// NewWindows returns the live system proxy of the current user.
//
// A restore is exact at the ProxyState level, not byte for byte in the
// registry: PROXY_TYPE_DIRECT is always written, an empty server or bypass
// list is written as an empty string (an absent ProxyServer/ProxyOverride
// value may come back present and empty), and AUTO_PROXY_URL with an empty
// URL is not kept (ProxyState has PAC in use only with a URL).
func NewWindows() sysapi.SystemProxy { return windowsProxy{} }

func (windowsProxy) Get() (sysapi.ProxyState, error) { return getConn("") }
func (windowsProxy) Set(s sysapi.ProxyState) error   { return setConn("", s) }
func (windowsProxy) Connections() ([]string, error)  { return rasEntries() }

func (windowsProxy) GetConn(name string) (sysapi.ProxyState, error) { return getConn(name) }
func (windowsProxy) SetConn(name string, s sysapi.ProxyState) error { return setConn(name, s) }

// newList builds an option list for the connection, pinning every Go
// object it points to for the duration of the call.
func newList(pin *runtime.Pinner, name string, opts []perConnOption) (*perConnOptionList, error) {
	l := &perConnOptionList{OptionCount: uint32(len(opts)), Options: &opts[0]}
	l.Size = uint32(unsafe.Sizeof(*l))
	if name != "" {
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return nil, err
		}
		pin.Pin(p)
		l.Connection = p
	}
	pin.Pin(&opts[0])
	pin.Pin(l)
	return l, nil
}

// query runs InternetQueryOptionW for opts on the connection and returns
// the string values (GlobalFree'd); a DWORD value stays in opts.
func query(name string, opts []perConnOption) ([]string, error) {
	if err := procInternetQueryOptionW.Find(); err != nil {
		return nil, err
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	l, err := newList(&pin, name, opts)
	if err != nil {
		return nil, err
	}
	size := l.Size
	r, _, e := procInternetQueryOptionW.Call(0, internetOptionPerConnectionOption,
		uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return nil, fmt.Errorf("InternetQueryOptionW: %w", e)
	}
	strs := make([]string, len(opts))
	for i, o := range opts {
		if o.Option == perConnFlags || o.Option == perConnFlagsUI {
			continue
		}
		if p := *opts[i].str(); p != nil { // allocated by WinINet with GlobalAlloc
			strs[i] = windows.UTF16PtrToString(p)
			procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
		}
	}
	return strs, nil
}

// getConn reads the flags as the settings UI shows them (FLAGS_UI): after a
// failed WPAD detection the plain FLAGS drop PROXY_TYPE_AUTO_DETECT while
// the user's checkbox is still on, and journaling that would switch it off
// on revert. FLAGS is the fallback where FLAGS_UI is not supported.
func getConn(name string) (sysapi.ProxyState, error) {
	opts := func(flags uint32) []perConnOption {
		return []perConnOption{
			{Option: flags},
			{Option: perConnProxyServer},
			{Option: perConnProxyBypass},
			{Option: perConnAutoconfigURL},
		}
	}
	o := opts(perConnFlagsUI)
	strs, err := query(name, o)
	if err != nil {
		o = opts(perConnFlags)
		if strs, err = query(name, o); err != nil {
			return sysapi.ProxyState{}, err
		}
	}
	flags := uint32(o[0].Value)
	s := sysapi.ProxyState{
		Enabled:    flags&proxyTypeProxy != 0,
		Server:     strs[1],
		Bypass:     splitBypass(strs[2]),
		AutoDetect: flags&proxyTypeAutoDetect != 0,
	}
	if flags&proxyTypeAutoProxyURL != 0 {
		s.PACURL = strs[3]
	}
	return s, nil
}

// queryFlags reads one flags option (FLAGS or FLAGS_UI) of the connection.
func queryFlags(name string, opt uint32) (uint32, error) {
	o := []perConnOption{{Option: opt}}
	if _, err := query(name, o); err != nil {
		return 0, err
	}
	return uint32(o[0].Value), nil
}

// setConn writes the flags, server and bypass, and the PAC URL when one is
// in use. Without one the stored URL is left as it is, only disabled, so a
// restore never loses a URL the user had switched off. The flags go to both
// FLAGS and FLAGS_UI, so that the FLAGS_UI read in getConn sees what we
// wrote; where WinINet rejects FLAGS_UI, the set is retried with FLAGS only.
func setConn(name string, s sysapi.ProxyState) error {
	if err := procInternetSetOptionW.Find(); err != nil {
		return err
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	str := func(v string) (uint64, error) {
		p, err := windows.UTF16PtrFromString(v) // "" is an empty string, not NULL
		if err != nil {
			return 0, err
		}
		pin.Pin(p)
		return uint64(uintptr(unsafe.Pointer(p))), nil
	}
	flags := uint32(proxyTypeDirect)
	if s.Enabled {
		flags |= proxyTypeProxy
	}
	if s.PACURL != "" {
		flags |= proxyTypeAutoProxyURL
	}
	if s.AutoDetect {
		flags |= proxyTypeAutoDetect
	}
	server, err := str(s.Server)
	if err != nil {
		return err
	}
	bypass, err := str(strings.Join(s.Bypass, ";"))
	if err != nil {
		return err
	}
	opts := []perConnOption{
		{Option: perConnFlagsUI, Value: uint64(flags)},
		{Option: perConnFlags, Value: uint64(flags)},
		{Option: perConnProxyServer, Value: server},
		{Option: perConnProxyBypass, Value: bypass},
	}
	if s.PACURL != "" {
		pac, err := str(s.PACURL)
		if err != nil {
			return err
		}
		opts = append(opts, perConnOption{Option: perConnAutoconfigURL, Value: pac})
	}
	if err := setOptions(&pin, name, opts); err != nil {
		if err = setOptions(&pin, name, opts[1:]); err != nil { // without FLAGS_UI
			return err
		}
	}
	for _, opt := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		if r, _, e := procInternetSetOptionW.Call(0, opt, 0, 0); r == 0 {
			return fmt.Errorf("InternetSetOptionW(%d): %w", opt, e)
		}
	}
	return nil
}

func setOptions(pin *runtime.Pinner, name string, opts []perConnOption) error {
	l, err := newList(pin, name, opts)
	if err != nil {
		return err
	}
	if r, _, e := procInternetSetOptionW.Call(0, internetOptionPerConnectionOption,
		uintptr(unsafe.Pointer(l)), uintptr(l.Size)); r == 0 {
		return fmt.Errorf("InternetSetOptionW: %w", e)
	}
	return nil
}

// splitBypass keeps the list exactly, empty items included, so that
// joining it again gives back the same string.
func splitBypass(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ";")
}

// rasEntries lists the dial-up/VPN entries of all phonebooks, without
// duplicates, in the order RAS reports them.
func rasEntries() ([]string, error) {
	if err := procRasEnumEntriesW.Find(); err != nil {
		return nil, fmt.Errorf("RasEnumEntriesW: %w (%w)", sysapi.ErrRASUnavailable, err)
	}
	buf := make([]rasEntryName, 1)
	for range 4 {
		esize := uint32(unsafe.Sizeof(buf[0]))
		buf[0].Size = esize
		cb := esize * uint32(len(buf))
		var n uint32
		r, _, _ := procRasEnumEntriesW.Call(0, 0,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&cb)), uintptr(unsafe.Pointer(&n)))
		switch r {
		case 0:
			var names []string
			for i := range min(int(n), len(buf)) {
				name := windows.UTF16ToString(buf[i].EntryName[:])
				if name != "" && !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
			return names, nil
		case errorBufferTooSmall:
			buf = make([]rasEntryName, cb/esize+1)
		default:
			return nil, fmt.Errorf("RasEnumEntriesW: %w", syscall.Errno(r))
		}
	}
	return nil, errors.New("RasEnumEntriesW: entry list keeps growing")
}
