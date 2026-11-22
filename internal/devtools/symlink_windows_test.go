package devtools

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isPrivilegeError(err error) bool { return errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) }
