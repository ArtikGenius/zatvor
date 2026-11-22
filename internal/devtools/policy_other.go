//go:build !windows

package devtools

// executionPolicy: there is no Windows PowerShell 5.1 here, so a missing
// pwsh5 profile is never created (an existing one is still edited).
func executionPolicy() string { return "" }
