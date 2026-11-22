//go:build !windows

package freeze

import "github.com/ArtikGenius/zatvor/internal/sysapi"

func suspend(sysapi.ProcID) error { return ErrUnsupported }
func resume(sysapi.ProcID) error  { return ErrUnsupported }
