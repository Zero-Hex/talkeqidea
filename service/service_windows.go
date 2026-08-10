//go:build windows

package service

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// NewManager returns the Windows Service Control Manager manager.
func NewManager() (Manager, error) {
	return &windowsManager{}, nil
}

type windowsManager struct{}

func (m *windowsManager) Kind() string { return "Windows Service" }

func (m *windowsManager) connect() (*mgr.Mgr, error) {
	scm, err := mgr.Connect()
	if err != nil {
		if !IsElevated() {
			return nil, fmt.Errorf("could not reach the Service Control Manager. %s", ElevationHint("modern-eq-chat", "service", "install"))
		}
		return nil, fmt.Errorf("connect to service manager: %w", err)
	}
	return scm, nil
}

func (m *windowsManager) Install(def Definition) error {
	def, err := Prepare(def)
	if err != nil {
		return err
	}
	if !IsElevated() {
		return fmt.Errorf("installing a service needs administrator rights. %s", ElevationHint(def.ExecutablePath, "service", "install"))
	}

	scm, err := m.connect()
	if err != nil {
		return err
	}
	defer scm.Disconnect()

	if existing, err := scm.OpenService(def.Name); err == nil {
		existing.Close()
		return fmt.Errorf("service %s is already installed", def.Name)
	}

	// The service is started with no arguments; it detects that it is running
	// under the SCM at startup. The working directory cannot be set through
	// the SCM, so the binary changes to its own directory instead — see
	// RunAsService.
	service, err := scm.CreateService(def.Name, def.ExecutablePath, mgr.Config{
		DisplayName:  def.DisplayName,
		Description:  def.Description,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer service.Close()

	// Restart on failure, matching the systemd unit's Restart=on-failure.
	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := service.SetRecoveryActions(recovery, uint32(86400)); err != nil {
		// Not fatal: the service is installed and will run, it just will not
		// restart itself after a crash.
		return fmt.Errorf("service installed, but setting restart-on-failure failed: %w", err)
	}
	return nil
}

func (m *windowsManager) Uninstall(name string) error {
	if !IsElevated() {
		return fmt.Errorf("removing a service needs administrator rights. %s", ElevationHint("modern-eq-chat", "service", "uninstall"))
	}

	scm, err := m.connect()
	if err != nil {
		return err
	}
	defer scm.Disconnect()

	service, err := scm.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s is not installed", name)
	}
	defer service.Close()

	// Stop first; a running service cannot be fully removed until it exits.
	if status, err := service.Query(); err == nil && status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err == nil {
			waitForState(service, svc.Stopped, 20*time.Second)
		}
	}

	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

func (m *windowsManager) Start(name string) error {
	if !IsElevated() {
		return fmt.Errorf("starting a service needs administrator rights. %s", ElevationHint("modern-eq-chat", "service", "start"))
	}

	scm, err := m.connect()
	if err != nil {
		return err
	}
	defer scm.Disconnect()

	service, err := scm.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s is not installed", name)
	}
	defer service.Close()

	if err := service.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func (m *windowsManager) Stop(name string) error {
	if !IsElevated() {
		return fmt.Errorf("stopping a service needs administrator rights. %s", ElevationHint("modern-eq-chat", "service", "stop"))
	}

	scm, err := m.connect()
	if err != nil {
		return err
	}
	defer scm.Disconnect()

	service, err := scm.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s is not installed", name)
	}
	defer service.Close()

	if _, err := service.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	waitForState(service, svc.Stopped, 20*time.Second)
	return nil
}

func (m *windowsManager) Status(name string) (Status, error) {
	status := Status{}

	scm, err := mgr.Connect()
	if err != nil {
		return status, fmt.Errorf("connect to service manager: %w", err)
	}
	defer scm.Disconnect()

	service, err := scm.OpenService(name)
	if err != nil {
		return status, nil
	}
	defer service.Close()

	status.IsInstalled = true

	current, err := service.Query()
	if err != nil {
		return status, fmt.Errorf("query service: %w", err)
	}
	status.IsRunning = current.State == svc.Running
	status.Detail = stateName(current.State)

	return status, nil
}

func waitForState(service *mgr.Service, want svc.State, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil || status.State == want {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continuing"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	default:
		return "unknown"
	}
}

// isElevated reports whether the process holds administrator rights.
func isElevated() bool {
	// Membership of the Administrators group is not enough on its own: with
	// UAC on, a non-elevated process carries the group as deny-only. Checking
	// the token this way reflects what the process can actually do.
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	token := windows.Token(0)
	isMember, err := token.IsMember(sid)
	if err != nil {
		return false
	}
	return isMember
}
