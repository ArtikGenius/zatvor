package devtools

import "golang.org/x/sys/windows/registry"

// executionPolicy returns the effective Windows PowerShell 5.1 execution
// policy for the current user, read-only from the registry in PowerShell's
// order of precedence: group policy (machine, then user), then the
// CurrentUser and LocalMachine settings. With none set, client Windows
// uses Restricted.
func executionPolicy() string {
	const (
		gpo   = `Software\Policies\Microsoft\Windows\PowerShell`
		shell = `Software\Microsoft\PowerShell\1\ShellIds\Microsoft.PowerShell`
	)
	for _, k := range []struct {
		root registry.Key
		path string
		gpo  bool
	}{
		{registry.LOCAL_MACHINE, gpo, true},
		{registry.CURRENT_USER, gpo, true},
		{registry.CURRENT_USER, shell, false},
		{registry.LOCAL_MACHINE, shell, false},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, verr := key.GetStringValue("ExecutionPolicy")
		enabled, _, eerr := key.GetIntegerValue("EnableScripts")
		key.Close()
		if k.gpo && eerr == nil && enabled == 0 {
			return "Restricted" // "Turn on Script Execution" is disabled
		}
		if verr == nil && v != "" {
			return v
		}
	}
	return "Restricted"
}
