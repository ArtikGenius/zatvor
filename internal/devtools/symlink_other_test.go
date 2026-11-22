//go:build !windows

package devtools

func isPrivilegeError(error) bool { return false }
