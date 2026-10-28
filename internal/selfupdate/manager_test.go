// manager_test.go
// Tests for release detection, the maintenance window, the pending-update
// state with automatic rollback, and the automatic install

package selfupdate

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"1.4.2": true, "v1.4.2": true, "2026.10": true,
		"dev": false, "0.0.0-dev": false, "ba179fc": false, "1234567": false,
		"1.4.2-3-gabc1234": false, "v1.4.2-dirty": false, "1..2": false, "": false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeAuto, "AUTO": ModeAuto, " notify ": ModeNotify, "off": ModeOff} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("ParseMode accepted an unknown mode")
	}
}

func TestWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }

	any, err := ParseWindow("")
	if err != nil || !any.Contains(at(13, 0)) || any.String() != "" {
		t.Fatalf("empty window = %+v, %v; want unrestricted", any, err)
	}

	night, err := ParseWindow("02:00-05:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		h, m int
		want bool
	}{{1, 59, false}, {2, 0, true}, {4, 59, true}, {5, 0, false}} {
		if got := night.Contains(at(c.h, c.m)); got != c.want {
			t.Errorf("02:00-05:00 contains %02d:%02d = %v, want %v", c.h, c.m, got, c.want)
		}
	}

	wrap, err := ParseWindow("22:30-04:00")
	if err != nil {
		t.Fatal(err)
	}
	if !wrap.Contains(at(23, 0)) || !wrap.Contains(at(3, 0)) || wrap.Contains(at(12, 0)) {
		t.Error("window across midnight misplaced")
	}
	if wrap.String() != "22:30-04:00" {
		t.Errorf("String() = %q", wrap.String())
	}

	for _, bad := range []string{"2-5", "02:00", "25:00-03:00", "02:00-02:00", "aa:bb-cc:dd"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("ParseWindow(%q) accepted", bad)
		}
	}
}

func TestRecoverRollsBackAfterFailedStarts(t *testing.T) {
	exe := useExecutable(t, "new")
	writeFile(t, exe+".bak", "old")
	if err := markPending("1.0.0", "1.1.0"); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= MaxStartAttempts; attempt++ {
		rec, err := Recover("1.1.0")
		if err != nil || rec.RolledBack || rec.Attempt != attempt {
			t.Fatalf("start %d: Recover = %+v, %v; want attempt %d without rollback", attempt, rec, err, attempt)
		}
	}
	rec, err := Recover("1.1.0")
	if err != nil || !rec.RolledBack || rec.From != "1.0.0" || rec.To != "1.1.0" {
		t.Fatalf("Recover = %+v, %v; want a rollback to 1.0.0", rec, err)
	}
	// The service logs Attempt-1 as the number of failed starts.
	if rec.Attempt-1 != MaxStartAttempts {
		t.Fatalf("Attempt = %d on rollback, want %d (start after %d failed ones)", rec.Attempt, MaxStartAttempts+1, MaxStartAttempts)
	}
	if got := readFile(t, exe); got != "old" {
		t.Fatalf("executable = %q after rollback, want the previous binary", got)
	}
	if skipped() != "1.1.0" {
		t.Fatalf("skipped() = %q, want the failed version", skipped())
	}
	// The restored binary starts without anything left to watch.
	if rec, err := Recover("1.0.0"); err != nil || rec.Attempt != 0 || rec.RolledBack {
		t.Fatalf("Recover after rollback = %+v, %v", rec, err)
	}
}

// After an automatic rollback a manual one must not put the failed version
// back: nothing pending would roll it back again, so the service would
// crash-loop. The automatic rollback keeps no backup, and Rollback refuses.
func TestManualRollbackAfterAutomaticRollback(t *testing.T) {
	cases := map[string]func() (Recovery, error){
		"crashing starts": func() (Recovery, error) {
			for range MaxStartAttempts {
				if _, err := Recover("1.1.0"); err != nil {
					return Recovery{}, err
				}
			}
			return Recover("1.1.0")
		},
		"failed start": func() (Recovery, error) { return AbortPending("1.1.0") },
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			exe := useExecutable(t, "new")
			writeFile(t, exe+".bak", "old")
			if err := markPending("1.0.0", "1.1.0"); err != nil {
				t.Fatal(err)
			}
			if rec, err := fail(); err != nil || !rec.RolledBack {
				t.Fatalf("automatic rollback = %+v, %v", rec, err)
			}
			if _, err := os.Stat(exe + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("backup after the automatic rollback (%v); it holds the failed version", err)
			}
			if got := readFile(t, exe+".failed"); got != "new" {
				t.Fatalf("%s.failed = %q, want the failed version", exe, got)
			}
			// The restored version starts, then an admin asks for a rollback.
			if _, err := Recover("1.0.0"); err != nil {
				t.Fatal(err)
			}
			err := Rollback()
			if err == nil || !strings.Contains(err.Error(), "1.1.0") || !strings.Contains(err.Error(), "rolled back automatically") {
				t.Fatalf("Rollback = %v, want a refusal naming 1.1.0", err)
			}
			if got := readFile(t, exe); got != "old" {
				t.Fatalf("executable = %q after the refused rollback, want the restored version", got)
			}

			// The next update drops the failed binary and makes a backup again,
			// which a manual rollback restores as usual.
			if err := replaceExecutable(exe, []byte("newer")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(exe + ".failed"); !os.IsNotExist(err) {
				t.Fatalf("failed version left behind by the next update (%v)", err)
			}
			if err := Rollback(); err != nil {
				t.Fatalf("Rollback after the next update: %v", err)
			}
			if got := readFile(t, exe); got != "old" {
				t.Fatalf("executable = %q, want the version before the update", got)
			}
		})
	}
}

// Without a backup and nothing rolled back, Rollback reports just that.
func TestRollbackWithoutBackup(t *testing.T) {
	useExecutable(t, "current")
	if err := Rollback(); err == nil || !strings.Contains(err.Error(), "no backup") {
		t.Fatalf("Rollback = %v, want no backup", err)
	}
}

func TestAbortPendingAndConfirm(t *testing.T) {
	exe := useExecutable(t, "new")
	writeFile(t, exe+".bak", "old")

	// Nothing pending: no-ops.
	if rec, err := AbortPending("1.1.0"); err != nil || rec.RolledBack {
		t.Fatalf("AbortPending without pending update = %+v, %v", rec, err)
	}
	if ok, err := Confirm("1.1.0"); err != nil || ok {
		t.Fatalf("Confirm without pending update = %v, %v", ok, err)
	}

	if err := markPending("1.0.0", "1.1.0"); err != nil {
		t.Fatal(err)
	}
	if ok, err := Confirm("1.1.0"); err != nil || !ok {
		t.Fatalf("Confirm = %v, %v; want the pending update confirmed", ok, err)
	}
	if _, err := os.Stat(exe + ".update.json"); !os.IsNotExist(err) {
		t.Fatal("state file left behind after a confirmed update")
	}
	// A confirmed update is no longer rolled back on failure.
	if rec, _ := AbortPending("1.1.0"); rec.RolledBack {
		t.Fatal("AbortPending rolled back a confirmed update")
	}

	if err := markPending("1.0.0", "1.1.0"); err != nil {
		t.Fatal(err)
	}
	rec, err := AbortPending("1.1.0")
	if err != nil || !rec.RolledBack {
		t.Fatalf("AbortPending = %+v, %v; want a rollback", rec, err)
	}
	if got := readFile(t, exe); got != "old" {
		t.Fatalf("executable = %q, want the previous binary", got)
	}
}

func TestManagerInstallsAutomaticallyAndRestarts(t *testing.T) {
	pub, priv := newKey(t)
	exe := useExecutable(t, "old")
	payload := []byte("new binary")
	m := Manifest{Version: "1.1.0", Binaries: map[string]Binary{
		// A relative URL resolves against the feed.
		platformKey(testProgram): {URL: "bin", SHA256: digest(payload)},
	}}
	f := signedFeed(t, priv, m)
	f.binary = payload
	srv := f.serve(t)

	var restarts, audits atomic.Int32
	mgr := NewManager(Options{
		Updater: New(srv.URL, pub), Mode: ModeAuto, Program: testProgram, Current: "1.0.0",
		Restart: func() { restarts.Add(1) },
		Audit:   func(context.Context, string, string, bool) { audits.Add(1) },
	})
	mgr.tick(context.Background())

	st := mgr.Status()
	if st.Installed != "1.1.0" || !st.Restarting || st.Err != nil {
		t.Fatalf("Status = %+v, want 1.1.0 installed and restarting", st)
	}
	if got := readFile(t, exe); got != string(payload) {
		t.Fatalf("executable = %q, want the new binary", got)
	}
	if audits.Load() != 1 {
		t.Errorf("audit called %d times, want 1", audits.Load())
	}
	deadline := time.Now().Add(5 * time.Second)
	for restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if restarts.Load() != 1 {
		t.Fatalf("restart called %d times, want 1", restarts.Load())
	}
	if rec, err := Recover("1.1.0"); err != nil || rec.Attempt != 1 {
		t.Fatalf("Recover = %+v, %v; want the install recorded as pending", rec, err)
	}
}

func TestManagerHoldsBackAutomaticInstall(t *testing.T) {
	pub, priv := newKey(t)
	m := Manifest{Version: "1.1.0", Binaries: map[string]Binary{platformKey(testProgram): {URL: "bin", SHA256: digest([]byte("x"))}}}

	cases := map[string]struct {
		opts    func(o *Options)
		setup   func()
		blocked string
	}{
		"notify mode":       {opts: func(o *Options) { o.Mode = ModeNotify }},
		"development build": {opts: func(o *Options) { o.Current = "1.0.0-4-gabc1234" }, blocked: BlockedDevBuild},
		"no service":        {opts: func(o *Options) { o.Restart = nil }, blocked: BlockedNoService},
		"rolled back":       {setup: func() { st, path, _ := loadState(); st.Skip = "1.1.0"; _ = saveState(path, st) }, blocked: BlockedRolledBack},
		"outside window": {opts: func(o *Options) {
			now := time.Now()
			from := now.Add(2 * time.Hour)
			w, _ := ParseWindow(from.Format("15:04") + "-" + from.Add(time.Hour).Format("15:04"))
			o.Window = w
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exe := useExecutable(t, "old")
			f := signedFeed(t, priv, m)
			f.binary = []byte("x")
			srv := f.serve(t)
			o := Options{Updater: New(srv.URL, pub), Mode: ModeAuto, Program: testProgram, Current: "1.0.0", Restart: func() {}}
			if tc.opts != nil {
				tc.opts(&o)
			}
			if tc.setup != nil {
				tc.setup()
			}
			mgr := NewManager(o)
			mgr.tick(context.Background())

			st := mgr.Status()
			if !st.Available || st.Installed != "" {
				t.Fatalf("Status = %+v, want the release reported but not installed", st)
			}
			if st.Blocked != tc.blocked {
				t.Errorf("Blocked = %q, want %q", st.Blocked, tc.blocked)
			}
			if got := readFile(t, exe); got != "old" {
				t.Fatalf("executable = %q, want it untouched", got)
			}
			if f.downloads.Load() != 0 {
				t.Fatal("binary downloaded although the install was held back")
			}
		})
	}
}

func TestManagerOffAndContainer(t *testing.T) {
	pub, _ := newKey(t)
	for name, o := range map[string]Options{
		"off":       {Updater: New("https://updates.example.com", pub), Mode: ModeOff},
		"container": {Updater: New("https://updates.example.com", pub), Mode: ModeAuto, Container: true},
	} {
		t.Run(name, func(t *testing.T) {
			mgr := NewManager(o)
			if mgr.Status().Enabled {
				t.Fatal("updater enabled")
			}
			if _, err := mgr.Check(context.Background()); err == nil {
				t.Fatal("Check ran on a disabled updater")
			}
		})
	}
}
