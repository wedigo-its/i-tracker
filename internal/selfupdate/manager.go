// manager.go
// Update modes, maintenance window and the background loop that checks the
// feed and installs releases automatically
// Version: 2026.10.28

package selfupdate

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"i-tracker/internal/i18n"
)

// Mode selects what the updater does with a newer release.
type Mode string

const (
	// ModeAuto installs a newer release on its own and restarts the service.
	ModeAuto Mode = "auto"
	// ModeNotify reports a newer release; an admin installs it.
	ModeNotify Mode = "notify"
	// ModeOff disables the updater.
	ModeOff Mode = "off"
)

// ParseMode validates a configured mode; empty means ModeAuto.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return ModeAuto, nil
	case ModeAuto, ModeNotify, ModeOff:
		return m, nil
	default:
		return "", i18n.E("err.update.mode", s)
	}
}

// Window is a daily maintenance window in local time, e.g. "02:00-05:00". It
// may wrap past midnight ("22:00-04:00"). The zero Window allows any time.
type Window struct {
	from, to int // minutes after midnight
	set      bool
}

// ParseWindow parses "HH:MM-HH:MM"; empty means no restriction.
func ParseWindow(s string) (Window, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Window{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return Window{}, i18n.E("err.update.window", s)
	}
	from, ok1 := parseClock(a)
	to, ok2 := parseClock(b)
	if !ok1 || !ok2 || from == to {
		return Window{}, i18n.E("err.update.window", s)
	}
	return Window{from: from, to: to, set: true}, nil
}

// parseClock reads "HH:MM" as minutes after midnight.
func parseClock(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hh, err := strconv.Atoi(h)
	if err != nil || hh < 0 || hh > 23 {
		return 0, false
	}
	mm, err := strconv.Atoi(m)
	if err != nil || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// Contains reports whether t falls into the window.
func (w Window) Contains(t time.Time) bool {
	if !w.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.from < w.to {
		return m >= w.from && m < w.to
	}
	return m >= w.from || m < w.to
}

// String renders the window as configured, or "" for none.
func (w Window) String() string {
	if !w.set {
		return ""
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.from/60, w.from%60, w.to/60, w.to%60)
}

// Reasons automatic installs are held back, as stable codes for the UI.
const (
	BlockedContainer   = "container"    // updated by pulling a new image
	BlockedDevBuild    = "dev_build"    // the running version is no release
	BlockedNoService   = "no_service"   // nothing would restart the process
	BlockedNotWritable = "not_writable" // the binary folder is read-only
	BlockedRolledBack  = "rolled_back"  // this release failed and was rolled back
)

// Options configures a Manager.
type Options struct {
	Updater  *Updater
	Mode     Mode
	Interval time.Duration
	Window   Window
	// Program selects the binary in the manifest, e.g. "i-tracker".
	Program string
	Current string
	// Restart restarts the process under its service manager once an update is
	// installed. Nil when nothing would bring the process back (a foreground
	// run): automatic installs are then held back and a manual install asks the
	// operator to restart.
	Restart func()
	// Container disables the updater: the image is updated instead.
	Container bool
	// Audit records an installed update; optional.
	Audit  func(ctx context.Context, from, to string, automatic bool)
	Logger *slog.Logger
}

// Status is a snapshot of the updater for the UI.
type Status struct {
	Mode      Mode
	Enabled   bool // a feed is configured and the mode is not off
	Container bool
	FeedURL   string
	Window    string
	Interval  time.Duration
	Current   string
	Latest    string
	Available bool
	Notes     string
	Checked   time.Time
	// Err is the last error; the web UI renders it with i18n.ErrorIn.
	Err error
	// Blocked is why an automatic install would not run (Blocked* code), or "".
	Blocked string
	// Installed is the version installed by this process, awaiting (or
	// undergoing) a restart.
	Installed  string
	Restarting bool
}

// Manager runs the periodic check and the automatic install.
type Manager struct {
	o  Options
	mu sync.Mutex
	st Status
	// rel is the latest verified release.
	rel        *Release
	installing bool
	// writable is the result of the folder probe, taken once.
	writable     error
	writableOnce sync.Once
}

// NewManager returns a Manager. A zero Interval means six hours.
func NewManager(o Options) *Manager {
	if o.Interval <= 0 {
		o.Interval = 6 * time.Hour
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	m := &Manager{o: o}
	m.st = Status{
		Mode:      o.Mode,
		Enabled:   o.Mode != ModeOff && !o.Container && o.Updater.Enabled(),
		Container: o.Container,
		FeedURL:   o.Updater.ChannelURL(o.Current),
		Window:    o.Window.String(),
		Interval:  o.Interval,
		Current:   o.Current,
	}
	return m
}

// Status returns the current state.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Mode: ModeOff}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.st
	st.Blocked = m.blocked()
	return st
}

// blocked returns why an automatic install would not run. Callers hold mu.
func (m *Manager) blocked() string {
	switch {
	case m.o.Container:
		return BlockedContainer
	case !IsRelease(m.o.Current):
		return BlockedDevBuild
	case m.o.Restart == nil:
		return BlockedNoService
	case m.st.Latest != "" && m.st.Latest == skipped():
		return BlockedRolledBack
	case m.probeWritable() != nil:
		return BlockedNotWritable
	}
	return ""
}

func (m *Manager) probeWritable() error {
	m.writableOnce.Do(func() { m.writable = Writable() })
	return m.writable
}

// Check queries the feed now.
func (m *Manager) Check(ctx context.Context) (Status, error) {
	if !m.Status().Enabled {
		return m.Status(), i18n.E("err.update.off")
	}
	rel, err := m.o.Updater.Check(ctx, m.o.Current)
	m.mu.Lock()
	m.st.Checked = time.Now()
	if err != nil {
		m.st.Err = err
	} else {
		m.st.Err = nil
		m.rel = &rel
		m.st.Latest = rel.Manifest.Version
		m.st.Notes = rel.Manifest.Notes
		m.st.Available = rel.Available
	}
	m.mu.Unlock()
	return m.Status(), err
}

// Install checks the feed and installs a newer release on an admin's request,
// even one an earlier automatic attempt rolled back. With a service manager
// the process restarts on its own afterwards.
func (m *Manager) Install(ctx context.Context) (Status, error) {
	st, err := m.Check(ctx)
	if err != nil {
		return st, err
	}
	if !st.Available {
		return st, nil
	}
	return m.install(ctx, false)
}

func (m *Manager) install(ctx context.Context, automatic bool) (Status, error) {
	m.mu.Lock()
	if m.installing || m.st.Installed != "" || m.rel == nil || !m.rel.Available {
		m.mu.Unlock()
		return m.Status(), nil
	}
	rel := *m.rel
	m.installing = true
	m.mu.Unlock()

	err := m.o.Updater.Apply(ctx, rel.Manifest, m.o.Program)
	if err == nil {
		err = markPending(m.o.Current, rel.Manifest.Version)
		if err != nil {
			m.o.Logger.Warn("self-update: could not record the pending update; automatic rollback is unavailable for it", "error", err)
			err = nil
		}
	}

	m.mu.Lock()
	m.installing = false
	if err != nil {
		m.st.Err = i18n.Wrap(err, "err.update.install", rel.Manifest.Version)
		m.mu.Unlock()
		return m.Status(), err
	}
	m.st.Err = nil
	m.st.Installed = rel.Manifest.Version
	m.st.Restarting = m.o.Restart != nil
	m.mu.Unlock()

	m.o.Logger.Warn("self-update installed", "from", m.o.Current, "to", rel.Manifest.Version, "automatic", automatic, "restart", m.o.Restart != nil)
	if m.o.Audit != nil {
		m.o.Audit(ctx, m.o.Current, rel.Manifest.Version, automatic)
	}
	if m.o.Restart != nil {
		// Let the HTTP response of a manual install reach the browser first.
		go func() {
			time.Sleep(2 * time.Second)
			m.o.Restart()
		}()
	}
	return m.Status(), nil
}

// Run confirms a pending update once this version has run for HealthyAfter,
// then checks the feed every Interval and, in auto mode, installs a newer
// release inside the maintenance window. It returns when ctx ends.
func (m *Manager) Run(ctx context.Context) {
	healthy := time.NewTimer(HealthyAfter)
	defer healthy.Stop()
	if !m.Status().Enabled {
		select {
		case <-ctx.Done():
		case <-healthy.C:
			m.confirm()
		}
		return
	}

	// The first check waits a minute so a crash-looping start does not hit the
	// feed on every attempt; later ticks are frequent enough to catch a short
	// maintenance window, while the feed itself is queried once per Interval.
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-healthy.C:
			m.confirm()
		case <-first.C:
			m.tick(ctx)
		case <-tick.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) confirm() {
	if ok, err := Confirm(m.o.Current); err != nil {
		m.o.Logger.Warn("self-update: could not confirm the update", "error", err)
	} else if ok {
		m.o.Logger.Info("self-update completed; the new version runs healthy", "version", m.o.Current)
	}
}

func (m *Manager) tick(ctx context.Context) {
	m.mu.Lock()
	due := time.Since(m.st.Checked) >= m.o.Interval
	m.mu.Unlock()
	if due {
		checkCtx, cancel := context.WithTimeout(ctx, time.Minute)
		st, err := m.Check(checkCtx)
		cancel()
		if err != nil {
			m.o.Logger.Warn("self-update check failed", "proxy", proxyFor(m.o.Updater.FeedURL()), "error", err)
			return
		}
		if st.Available {
			m.o.Logger.Info("a newer release is available", "current", m.o.Current, "latest", st.Latest, "mode", m.o.Mode)
		}
	}

	st := m.Status()
	if m.o.Mode != ModeAuto || !st.Available || st.Installed != "" || !m.o.Window.Contains(time.Now()) {
		return
	}
	if st.Blocked != "" {
		if due {
			m.o.Logger.Info("self-update: automatic install held back", "reason", st.Blocked, "latest", st.Latest)
		}
		return
	}
	installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := m.install(installCtx, true); err != nil {
		m.o.Logger.Error("self-update: automatic install failed", "latest", st.Latest, "error", err)
	}
}

// proxyFor names the proxy net/http uses for rawURL (password hidden), for
// the log of a failed check.
func proxyFor(rawURL string) string {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	if p, err := http.ProxyFromEnvironment(req); err == nil && p != nil {
		return p.Redacted()
	}
	return "none (direct connection)"
}
