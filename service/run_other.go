//go:build !windows

package service

import (
	"os"
	"os/signal"
	"syscall"
)

// IsRunningAsService reports whether a service manager started this process.
//
// On Linux there is nothing to detect: a systemd unit runs the binary exactly
// as a shell would, with the working directory set by the unit file. The
// process needs no special mode.
func IsRunningAsService() bool { return false }

// Run executes serve, closing the stop channel when the process is asked to
// exit.
//
// Both SIGINT and SIGTERM are handled: SIGINT for an operator pressing ctrl-c,
// SIGTERM because that is what systemd sends. Missing the second would make
// every restart a hard kill.
func Run(name string, serve func(stop <-chan struct{}) error) error {
	stop := make(chan struct{})

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-signals
		close(stop)
	}()

	return serve(stop)
}
