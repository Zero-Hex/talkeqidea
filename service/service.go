// Package service installs, removes and controls TalkEQ as a background
// service on Windows and Linux.
//
// Running under a service manager is what makes an unattended relay actually
// survive a reboot, and it is also where several cross-platform footguns live:
// a service starts with a working directory that is not where the operator
// installed the files, with no console to print to, and often as a different
// user than the one who ran setup. Everything here exists to get those three
// things right.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Definition describes a service to install.
type Definition struct {
	// Name is the service identifier, e.g. "talkeq-hub".
	Name string
	// DisplayName is shown in service managers.
	DisplayName string
	// Description explains the service.
	Description string
	// ExecutablePath is the binary to run. Defaults to the running one.
	ExecutablePath string
	// WorkingDirectory is where the service runs.
	//
	// This matters more than it looks: talkeq.conf, the roster, the
	// certificate and the log are all resolved relative to the working
	// directory. A service manager would otherwise start the process in / or
	// C:\Windows\System32, where none of them exist.
	WorkingDirectory string
	// User is the account to run as on Linux. Empty means root, which is
	// discouraged and warned about.
	User string
}

// Status describes an installed service.
type Status struct {
	IsInstalled bool
	IsRunning   bool
	// Detail is a human-readable line from the platform's service manager.
	Detail string
}

// Manager controls services on the current platform.
type Manager interface {
	// Install registers the service.
	Install(def Definition) error
	// Uninstall removes it, stopping it first if needed.
	Uninstall(name string) error
	// Start launches an installed service.
	Start(name string) error
	// Stop halts a running service.
	Stop(name string) error
	// Status reports what the service manager knows.
	Status(name string) (Status, error)
	// Kind names the platform mechanism, e.g. "systemd".
	Kind() string
}

// ErrUnsupported is returned on platforms with no service integration.
type ErrUnsupported struct {
	GOOS string
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("service installation is not supported on %s; run the binary directly or use your platform's process supervisor", e.GOOS)
}

// Prepare fills in the parts of a definition that can be derived from the
// running process, and checks that the result is usable.
func Prepare(def Definition) (Definition, error) {
	if def.Name == "" {
		return def, fmt.Errorf("service name is required")
	}
	if def.DisplayName == "" {
		def.DisplayName = def.Name
	}

	if def.ExecutablePath == "" {
		exe, err := os.Executable()
		if err != nil {
			return def, fmt.Errorf("locate this executable: %w", err)
		}
		// Resolve symlinks so a service does not silently follow a link that
		// is later repointed.
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		def.ExecutablePath = exe
	}
	absExe, err := filepath.Abs(def.ExecutablePath)
	if err != nil {
		return def, fmt.Errorf("resolve executable path: %w", err)
	}
	def.ExecutablePath = absExe

	if def.WorkingDirectory == "" {
		// Default to where the binary lives rather than the current directory:
		// an operator may install from anywhere, but the config sits beside
		// the executable.
		def.WorkingDirectory = filepath.Dir(def.ExecutablePath)
	}
	absDir, err := filepath.Abs(def.WorkingDirectory)
	if err != nil {
		return def, fmt.Errorf("resolve working directory: %w", err)
	}
	def.WorkingDirectory = absDir

	if fi, err := os.Stat(def.WorkingDirectory); err != nil || !fi.IsDir() {
		return def, fmt.Errorf("working directory %s does not exist", def.WorkingDirectory)
	}

	return def, nil
}

// IsElevated reports whether the process can install a system service.
func IsElevated() bool {
	return isElevated()
}

// ElevationHint explains how to re-run with the privileges needed.
func ElevationHint(args ...string) string {
	command := strings.Join(args, " ")

	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf("Right click the program and choose 'Run as administrator', then run: %s", command)
	default:
		return fmt.Sprintf("Run it with sudo: sudo %s", command)
	}
}
