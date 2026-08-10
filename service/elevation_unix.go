//go:build !windows

package service

import "os"

// isElevated reports whether the process is running as root.
//
// Effective rather than real UID: that is what the kernel checks when the
// process tries to write into /etc/systemd/system.
func isElevated() bool {
	return os.Geteuid() == 0
}
