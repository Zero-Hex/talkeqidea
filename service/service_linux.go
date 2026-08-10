//go:build linux

package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// NewManager returns the systemd manager.
//
// Both checks matter. Plenty of systems have the systemctl binary without
// systemd as PID 1 - Docker containers, WSL1, some minimal VPS images - and
// there systemctl exists but every call fails with "System has not been booted
// with systemd". Testing for /run/systemd/system is what systemd's own
// sd_booted() does, and it is the difference between a clear message and a
// confusing failure halfway through an install.
func NewManager() (Manager, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, fmt.Errorf("systemctl was not found; this system does not appear to use systemd.\n" +
			"Run the binary directly, or supervise it with whatever your system uses")
	}
	if !isSystemdBooted() {
		return nil, fmt.Errorf("systemctl is present but systemd is not running as PID 1.\n" +
			"This is normal in a container or under WSL1. Run the binary directly, or\n" +
			"supervise it with your container runtime's restart policy")
	}
	return &systemdManager{}, nil
}

// isSystemdBooted reports whether systemd is the init system, the same way
// systemd's sd_booted() does.
func isSystemdBooted() bool {
	fi, err := os.Stat("/run/systemd/system")
	return err == nil && fi.IsDir()
}

type systemdManager struct{}

func (m *systemdManager) Kind() string { return "systemd" }

const unitDir = "/etc/systemd/system"

// unitTemplate is deliberately hardened beyond the minimum.
//
// The relay reaches a telnet console, so a compromise of this process is
// already serious; the sandboxing directives below cost nothing and remove
// most of what an attacker could do next. ProtectSystem and PrivateTmp are the
// important ones, and ReadWritePaths keeps the working directory writable for
// the config, roster and log.
var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description={{.Description}}
Documentation=https://github.com/xackery/talkeq
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.ExecutablePath}}
WorkingDirectory={{.WorkingDirectory}}
{{if .User}}User={{.User}}
{{end}}Restart=on-failure
RestartSec=10s

# Hardening. The process needs its working directory and outbound network,
# and nothing else.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths={{.WorkingDirectory}}
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
`))

func (m *systemdManager) unitPath(name string) string {
	return filepath.Join(unitDir, name+".service")
}

func (m *systemdManager) Install(def Definition) error {
	def, err := Prepare(def)
	if err != nil {
		return err
	}
	if !IsElevated() {
		return fmt.Errorf("installing a systemd unit needs root. %s", ElevationHint(def.ExecutablePath, "service", "install"))
	}

	if def.Description == "" {
		def.Description = def.DisplayName
	}

	buf := &bytes.Buffer{}
	if err := unitTemplate.Execute(buf, def); err != nil {
		return fmt.Errorf("render unit: %w", err)
	}

	path := m.unitPath(def.Name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	// If either call fails the install did not take effect, so remove the unit
	// rather than leaving a file that looks installed but is not enabled.
	if err := run("systemctl", "daemon-reload"); err != nil {
		os.Remove(path)
		return err
	}
	if err := run("systemctl", "enable", def.Name); err != nil {
		os.Remove(path)
		_ = run("systemctl", "daemon-reload")
		return err
	}
	return nil
}

func (m *systemdManager) Uninstall(name string) error {
	if !IsElevated() {
		return fmt.Errorf("removing a systemd unit needs root. %s", ElevationHint("talkeq", "service", "uninstall"))
	}

	// Ignore failures here: the service may already be stopped or disabled,
	// and neither should block removal.
	_ = run("systemctl", "stop", name)
	_ = run("systemctl", "disable", name)

	path := m.unitPath(name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return run("systemctl", "daemon-reload")
}

func (m *systemdManager) Start(name string) error {
	if !IsElevated() {
		return fmt.Errorf("starting a service needs root. %s", ElevationHint("talkeq", "service", "start"))
	}
	return run("systemctl", "start", name)
}

func (m *systemdManager) Stop(name string) error {
	if !IsElevated() {
		return fmt.Errorf("stopping a service needs root. %s", ElevationHint("talkeq", "service", "stop"))
	}
	return run("systemctl", "stop", name)
}

func (m *systemdManager) Status(name string) (Status, error) {
	status := Status{}

	if _, err := os.Stat(m.unitPath(name)); err != nil {
		if os.IsNotExist(err) {
			return status, nil
		}
		return status, fmt.Errorf("stat unit: %w", err)
	}
	status.IsInstalled = true

	// systemctl exits non-zero when a service is not active, which is
	// information rather than an error.
	out, _ := exec.Command("systemctl", "is-active", name).Output()
	state := strings.TrimSpace(string(out))
	status.IsRunning = state == "active"
	status.Detail = state

	return status, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
	}
	return nil
}
