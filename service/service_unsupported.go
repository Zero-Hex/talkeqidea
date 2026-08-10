//go:build !linux && !windows

package service

import "runtime"

// NewManager reports that this platform has no service integration.
//
// macOS and the BSDs can still run the relay perfectly well; they just need
// launchd or rc.d set up by hand, which is not something to guess at on the
// operator's behalf.
func NewManager() (Manager, error) {
	return nil, &ErrUnsupported{GOOS: runtime.GOOS}
}
