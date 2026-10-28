package iperf

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"i-tracker/internal/i18n"
)

// installTimeout begrenzt eine Installation.
const installTimeout = 10 * time.Minute

var (
	// ErrInstallRunning reports that an installation is already running.
	ErrInstallRunning = i18n.E("err.install.running")
	// ErrNotInstallable reports that iperf3 cannot be installed here (already
	// present, path configured or no package manager).
	ErrNotInstallable = i18n.E("err.install.not_installable")
)

// Status beschreibt die iperf3-Installation, die der Runner verwendet.
type Status struct {
	Path       string `json:"path"`
	Version    string `json:"version,omitempty"`
	Available  bool   `json:"available"`  // vorhanden und neu genug
	Installing bool   `json:"installing"` // Installation läuft
	// Installable ist true, wenn iperf3 fehlt und über den Paketmanager
	// installiert werden kann; InstallCommand nennt die Befehle dafür.
	Installable    bool   `json:"installable"`
	InstallCommand string `json:"install_command,omitempty"`
	// Error is Err in English; the web UI renders Err in the request language.
	Error string `json:"error,omitempty"`
	Err   error  `json:"-"`
}

// setErr sets Err and its English text.
func (st *Status) setErr(err error) {
	st.Err = err
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
	}
}

// Austauschbar für Tests: Ermittlung der Installationsbefehle und Installation.
var (
	installPlan = func() ([][]string, error) {
		return installCommands(runtime.GOOS, exec.LookPath, os.Geteuid() == 0)
	}
	installFunc = Install
)

// Status liefert den aktuellen iperf3-Status.
func (r *Runner) Status() Status {
	r.cmdMu.RLock()
	defer r.cmdMu.RUnlock()
	return r.status
}

func (r *Runner) commandLine() []string {
	r.cmdMu.RLock()
	defer r.cmdMu.RUnlock()
	return r.command
}

// refresh prüft binary, übernimmt es und ermittelt, ob iperf3 installierbar ist.
func (r *Runner) refresh(binary string) error {
	r.cmdMu.RLock()
	configured := r.configured
	r.cmdMu.RUnlock()

	version, err := CheckVersion(binary)
	st := Status{Path: binary, Version: version, Available: err == nil}
	if err != nil {
		st.setErr(err)
		if errors.Is(err, ErrNotInstalled) && configured == "" {
			if cmds, planErr := installPlan(); planErr == nil {
				st.Installable = true
				parts := make([]string, len(cmds))
				for i, c := range cmds {
					parts[i] = strings.Join(c, " ")
				}
				st.InstallCommand = strings.Join(parts, " && ")
			} else {
				st.setErr(i18n.E("err.with_reason", err, planErr))
			}
		}
	}
	r.cmdMu.Lock()
	r.command = []string{binary}
	r.status = st
	r.cmdMu.Unlock()
	return err
}

// Prepare sucht iperf3 und prüft die Version. Fehlt iperf3, wird es nur bei
// autoInstall sofort installiert; sonst bleibt die Installation der
// Oberfläche überlassen (InstallAsync nach Bestätigung durch einen Admin).
// Eine zu alte Version wird nur gemeldet, ein konfigurierter Pfad nie ersetzt.
func (r *Runner) Prepare(configured string, autoInstall bool) {
	r.cmdMu.Lock()
	r.configured = configured
	r.cmdMu.Unlock()

	binary := FindBinary(configured)
	err := r.refresh(binary)
	if err == nil {
		slog.Info("iperf3 found", "path", binary, "version", r.Status().Version)
		return
	}
	if autoInstall && r.Status().Installable {
		slog.Info("iperf3 not found, starting automatic installation")
		r.InstallAsync()
		return
	}
	slog.Warn("iperf3 not usable, tests will fail", "path", binary, "error", err,
		"installable", r.Status().Installable)
}

// InstallAsync installiert iperf3 im Hintergrund über den Paketmanager und
// verwendet es anschließend ohne Neustart.
func (r *Runner) InstallAsync() error {
	r.cmdMu.Lock()
	switch {
	case r.status.Installing:
		r.cmdMu.Unlock()
		return ErrInstallRunning
	case !r.status.Installable:
		r.cmdMu.Unlock()
		return ErrNotInstallable
	}
	r.status.Installing = true
	r.status.setErr(nil)
	r.cmdMu.Unlock()
	slog.Info("Installing iperf3", "command", r.Status().InstallCommand)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(r.ctx, installTimeout)
		defer cancel()
		installErr := installFunc(ctx)

		// Auch nach einem Fehler neu suchen: winget meldet z. B. einen Fehler,
		// wenn das Paket bereits installiert ist.
		binary := FindBinary("")
		if err := r.refresh(binary); err != nil {
			switch {
			case installErr != nil:
				err = i18n.Wrap(installErr, "err.install.failed")
			case errors.Is(err, ErrNotInstalled):
				err = i18n.E("err.install.not_found_after")
			}
			r.cmdMu.Lock()
			r.status.setErr(err)
			r.cmdMu.Unlock()
			slog.Error("iperf3 could not be installed", "error", err)
			return
		}
		slog.Info("iperf3 installed", "path", binary, "version", r.Status().Version)
	}()
	return nil
}
