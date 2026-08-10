//go:build windows

package service

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/svc"
)

// IsRunningAsService reports whether the Service Control Manager started this
// process, as opposed to an operator running it from a console.
func IsRunningAsService() bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isService
}

// Run executes serve, closing the stop channel when the process is asked to
// exit.
//
// Under the SCM this also fixes the working directory. Windows starts services
// in C:\Windows\System32 with no way to configure otherwise, so without this
// the process would look for modern-eq-chat.conf there, fail, and write its log into a
// system directory.
func Run(name string, serve func(stop <-chan struct{}) error) error {
	if !IsRunningAsService() {
		return runConsole(serve)
	}

	if err := chdirToExecutable(); err != nil {
		return err
	}
	return svc.Run(name, &handler{serve: serve})
}

func runConsole(serve func(stop <-chan struct{}) error) error {
	stop := make(chan struct{})

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-signals
		close(stop)
	}()

	return serve(stop)
}

func chdirToExecutable() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := os.Chdir(filepath.Dir(exe)); err != nil {
		return fmt.Errorf("change to %s: %w", filepath.Dir(exe), err)
	}
	return nil
}

// handler bridges the SCM's control protocol to the stop channel.
type handler struct {
	serve func(stop <-chan struct{}) error
}

func (h *handler) Execute(args []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	stop := make(chan struct{})
	done := make(chan error, 1)

	go func() { done <- h.serve(stop) }()

	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	closed := false
	closeOnce := func() {
		if !closed {
			closed = true
			close(stop)
		}
	}

	for {
		select {
		case err := <-done:
			// The service exited on its own. A non-zero code tells the SCM to
			// apply the configured restart actions.
			if err != nil {
				return false, 1
			}
			return false, 0

		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus

			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				closeOnce()

				// Wait for a clean exit so agents see a proper close rather
				// than a reset connection.
				<-done
				return false, 0
			}
		}
	}
}
