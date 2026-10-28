//go:build windows

// recovery_windows.go
// Service recovery actions and the detached restart used by the self-updater
// Version: 2026.10.28

package svc

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// EnsureRecovery makes the Service Control Manager restart the service after a
// crash. The self-updater depends on it: a freshly installed version that
// crashes is started again until its start attempts run out and the previous
// binary is restored. It is applied on install and on every service start, so
// installations made before this existed get it too.
func EnsureRecovery() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, uint32((24 * time.Hour).Seconds()))
}

// RestartDetached restarts the service from a separate process: the binary
// (already the updated one) runs "service restart", which stops this process
// gracefully through the SCM and starts the service again. A service cannot
// do that itself, since stopping it ends the code doing the restart.
func RestartDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "service", "restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
