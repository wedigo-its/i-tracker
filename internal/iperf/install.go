package iperf

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"

	"i-tracker/internal/i18n"
)

// Paketmanager in der Reihenfolge, in der sie unter Linux gesucht werden.
var linuxPackageManagers = []struct {
	tool string
	cmds [][]string
}{
	{"apt-get", [][]string{{"apt-get", "update"}, {"apt-get", "install", "-y", "iperf3"}}},
	{"dnf", [][]string{{"dnf", "install", "-y", "iperf3"}}},
	{"yum", [][]string{{"yum", "install", "-y", "iperf3"}}},
	{"zypper", [][]string{{"zypper", "--non-interactive", "install", "iperf3"}}},
	{"apk", [][]string{{"apk", "add", "--no-cache", "iperf3"}}},
	{"pacman", [][]string{{"pacman", "-S", "--noconfirm", "iperf3"}}},
}

// installCommands liefert die Befehle, mit denen iperf3 auf goos installiert
// wird. lookPath sucht Werkzeuge, isRoot gibt an, ob Root-Rechte bestehen.
// Ohne Root wird unter Linux sudo im nicht-interaktiven Modus (-n) genutzt:
// verlangt sudo ein Passwort, schlägt die Installation fehl statt zu hängen.
func installCommands(goos string, lookPath func(string) (string, error), isRoot bool) ([][]string, error) {
	has := func(tool string) bool { _, err := lookPath(tool); return err == nil }
	switch goos {
	case "windows":
		if !has("winget") {
			return nil, i18n.E("err.install.no_winget")
		}
		return [][]string{{"winget", "install", "--id", "ar51an.iPerf3", "-e", "--silent",
			"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}}, nil
	case "darwin":
		if !has("brew") {
			return nil, i18n.E("err.install.no_brew")
		}
		return [][]string{{"brew", "install", "iperf3"}}, nil
	case "linux":
		for _, pm := range linuxPackageManagers {
			if !has(pm.tool) {
				continue
			}
			if isRoot {
				return pm.cmds, nil
			}
			if !has("sudo") {
				return nil, i18n.E("err.install.no_sudo", pm.tool)
			}
			cmds := make([][]string, len(pm.cmds))
			for i, c := range pm.cmds {
				cmds[i] = append([]string{"sudo", "-n"}, c...)
			}
			return cmds, nil
		}
		return nil, i18n.E("err.install.no_package_manager")
	}
	return nil, i18n.E("err.install.unsupported_os", goos)
}

// Install installiert iperf3 über den Paketmanager des Systems (Windows:
// winget, Linux: apt-get/dnf/yum/zypper/apk/pacman, macOS: brew).
func Install(ctx context.Context) error {
	cmds, err := installPlan()
	if err != nil {
		return err
	}
	for _, c := range cmds {
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return i18n.E("err.install.command", strings.Join(c, " "), err, lastLines(out.String(), 3))
		}
	}
	return nil
}

// lastLines liefert die letzten n nicht leeren Zeilen einer Ausgabe.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
