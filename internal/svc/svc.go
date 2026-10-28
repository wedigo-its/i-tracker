// Package svc manages the OS service (Windows service, systemd, launchd) via
// github.com/kardianos/service, modelled on Spherifyer. The installed service
// runs the binary with "service-run" and the config path set at install time.
package svc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kardianos/service"
)

// Name is the service identifier.
const Name = "i-tracker"

// config returns the service definition; cfgPath only matters for install
// (start/stop/status find the service by name).
func config(cfgPath string) *service.Config {
	wd := ""
	if exe, err := os.Executable(); err == nil {
		wd = filepath.Dir(exe)
	}
	args := []string{"service-run"}
	if cfgPath != "" {
		args = append(args, "-config", cfgPath)
	}
	cfg := &service.Config{
		Name:             Name,
		DisplayName:      "i-Tracker",
		Description:      "i-Tracker – scheduled iperf3 tests with a web interface.",
		Arguments:        args,
		WorkingDirectory: wd,
		Option:           service.KeyValue{},
	}
	// systemd: start after the network (on Windows these would be service names).
	// Restart only after a failure: the self-updater exits with an error to be
	// restarted (and rolled back), a stop by the admin stays a stop.
	if runtime.GOOS == "linux" {
		cfg.Dependencies = []string{"After=network-online.target", "Wants=network-online.target"}
		cfg.Option["Restart"] = "on-failure"
	}
	// Optional service account from the environment (as in Spherifyer).
	if user := os.Getenv("SERVICE_ACCOUNT_USER"); user != "" {
		cfg.UserName = user
		if pw := os.Getenv("SERVICE_ACCOUNT_PASSWORD"); pw != "" {
			cfg.Option["Password"] = pw
		}
	}
	return cfg
}

// New builds the service for prg (invoked with "service-run").
func New(prg service.Interface, cfgPath string) (service.Service, error) {
	return service.New(prg, config(cfgPath))
}

// Interactive reports whether the program runs in a console, not under the service manager.
func Interactive() bool { return service.Interactive() }

func control(action, cfgPath string) error {
	s, err := service.New(noopProgram{}, config(cfgPath))
	if err != nil {
		return err
	}
	return service.Control(s, action)
}

// Install registers the service; cfgPath is passed as -config.
func Install(cfgPath string) error { return control("install", cfgPath) }

// Uninstall removes the service.
func Uninstall() error { return control("uninstall", "") }

// Start starts the installed service.
func Start() error { return control("start", "") }

// Stop stops the service.
func Stop() error { return control("stop", "") }

// Restart restarts the service.
func Restart() error { return control("restart", "") }

// Status reports "running", "stopped", "not installed" or "unknown".
func Status() (string, error) {
	s, err := service.New(noopProgram{}, config(""))
	if err != nil {
		return "", err
	}
	st, err := s.Status()
	if err != nil {
		if errors.Is(err, service.ErrNotInstalled) {
			return "not installed", nil
		}
		return "", err
	}
	switch st {
	case service.StatusRunning:
		return "running", nil
	case service.StatusStopped:
		return "stopped", nil
	default:
		return "unknown", nil
	}
}

// Platform returns the service system, e.g. "windows-service" or "linux-systemd".
func Platform() string { return service.Platform() }

type noopProgram struct{}

func (noopProgram) Start(service.Service) error { return nil }
func (noopProgram) Stop(service.Service) error  { return nil }
