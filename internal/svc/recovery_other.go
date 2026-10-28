//go:build !windows

// recovery_other.go
// Service recovery on systems other than Windows
// Version: 2026.10.28

package svc

import (
	"errors"
	"os"
	"os/exec"
)

// EnsureRecovery is a no-op: the systemd unit restarts the service when it
// exits with an error (Restart=on-failure, see config).
func EnsureRecovery() error { return nil }

// RestartDetached asks systemd to restart the unit without waiting
// (systemctl restart --no-block): systemd stops this process gracefully and
// starts the updated binary at once. Exiting instead would leave the restart
// to Restart=on-failure, which waits RestartSec=120 (fixed in the unit
// kardianos/service writes). Elsewhere, and outside systemd, the caller exits
// with an error and the service manager starts the service again.
func RestartDetached() error {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("detached restart needs Windows or systemd")
	}
	err := exec.Command("systemctl", "--no-block", "restart", Name+".service").Run()
	// Stopping the unit ends every process in it, systemctl too if it has not
	// returned yet; the queued restart goes ahead regardless.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && !exitErr.Exited() {
		return nil
	}
	return err
}
