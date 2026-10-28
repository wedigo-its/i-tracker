// state.go
// Update state kept next to the executable: the pending update awaiting its
// first healthy run, and a version that was rolled back and is skipped.
// Version: 2026.10.28

package selfupdate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"i-tracker/internal/i18n"
)

// MaxStartAttempts is how often a freshly installed version may start without
// reaching a healthy run before it is rolled back.
const MaxStartAttempts = 3

// HealthyAfter is how long a new version has to run before its update counts
// as successful and the pending marker is cleared.
const HealthyAfter = 2 * time.Minute

// state is persisted as <exe>.update.json. It lives next to the binary because
// that folder is writable whenever an update could be installed at all, and it
// belongs to the binary rather than to one of possibly several configs.
type state struct {
	Pending *pending `json:"pending,omitempty"`
	// Skip is a version that failed to start and was rolled back. Automatic
	// updates leave it alone; a manual install or a newer release clears it.
	Skip string `json:"skip,omitempty"`
}

type pending struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Attempts  int       `json:"attempts"`
	Installed time.Time `json:"installed"`
}

func statePath() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	return exe + ".update.json", nil
}

func loadState() (state, string, error) {
	path, err := statePath()
	if err != nil {
		return state{}, "", err
	}
	var st state
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, path, nil
	}
	if err != nil {
		return st, path, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		// A damaged file must not block the service; start over.
		return state{}, path, nil
	}
	return st, path, nil
}

func saveState(path string, st state) error {
	if st.Pending == nil && st.Skip == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// markPending records that version to was just installed over from.
func markPending(from, to string) error {
	st, path, err := loadState()
	if err != nil {
		return err
	}
	st.Pending = &pending{From: from, To: to, Installed: time.Now().UTC()}
	if st.Skip == to {
		st.Skip = ""
	}
	return saveState(path, st)
}

func clearPending() {
	if st, path, err := loadState(); err == nil && st.Pending != nil {
		st.Pending = nil
		_ = saveState(path, st)
	}
}

// skipped returns the version automatic updates must not install again.
func skipped() string {
	st, _, _ := loadState()
	return st.Skip
}

// Recovery reports what Recover did.
type Recovery struct {
	// RolledBack is set when the previous binary was restored. The running
	// process is still the failed version: the caller exits with an error so
	// the service manager starts the restored one.
	RolledBack bool
	// Attempt is the start attempt of a pending update (0 when none is pending).
	// A rollback by Recover happens on start MaxStartAttempts+1, after
	// Attempt-1 starts that did not run healthy.
	Attempt int
	From    string
	To      string
}

// Recover runs first when the service starts. It counts the start attempts of
// a freshly installed version and rolls it back once MaxStartAttempts starts
// passed without a healthy run, which covers crashes the process cannot
// report itself.
func Recover(current string) (Recovery, error) {
	st, path, err := loadState()
	if err != nil || st.Pending == nil {
		return Recovery{}, err
	}
	p := *st.Pending
	if p.To != current {
		// Another binary than the installed one is running: a manual rollback,
		// or a release whose binary reports another version than its manifest.
		// Either way the release must not be installed again automatically,
		// or the service would reinstall it on every start.
		st.Pending = nil
		st.Skip = p.To
		return Recovery{}, saveState(path, st)
	}
	p.Attempts++
	st.Pending = &p
	if p.Attempts > MaxStartAttempts {
		return rollbackPending(path, st)
	}
	return Recovery{Attempt: p.Attempts, From: p.From, To: p.To}, saveState(path, st)
}

// AbortPending rolls back at once when a freshly installed version fails to
// start cleanly (bad config for the new version, port conflict, ...). It does
// nothing when no update of the running version is pending.
func AbortPending(current string) (Recovery, error) {
	st, path, err := loadState()
	if err != nil || st.Pending == nil || st.Pending.To != current {
		return Recovery{}, err
	}
	return rollbackPending(path, st)
}

// rollbackPending restores the backup and sets the failed version aside as
// <exe>.failed rather than as the new backup: a later manual rollback must
// not reinstall it, as no pending update would roll it back again.
func rollbackPending(path string, st state) (Recovery, error) {
	p := *st.Pending
	exe, err := executable()
	if err != nil {
		return Recovery{}, err
	}
	if err := replaceWithBackup(exe, exe+".failed"); err != nil {
		return Recovery{}, i18n.Wrap(err, "err.update.rollback", p.To, p.From)
	}
	st.Pending = nil
	st.Skip = p.To
	if err := saveState(path, st); err != nil {
		return Recovery{RolledBack: true, Attempt: p.Attempts, From: p.From, To: p.To}, err
	}
	return Recovery{RolledBack: true, Attempt: p.Attempts, From: p.From, To: p.To}, nil
}

// Confirm marks the running version as healthy: a pending update of it is
// complete and keeps its backup only for a manual rollback.
func Confirm(current string) (bool, error) {
	st, path, err := loadState()
	if err != nil || st.Pending == nil || st.Pending.To != current {
		return false, err
	}
	st.Pending = nil
	return true, saveState(path, st)
}
