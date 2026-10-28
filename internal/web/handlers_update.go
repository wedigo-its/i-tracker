// handlers_update.go
// Status and manual control of the self-updater for the "Software update" card
// Version: 2026.10.28

package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
	"i-tracker/internal/selfupdate"
)

// updateStatus is the JSON form of selfupdate.Status.
type updateStatus struct {
	Mode       string `json:"mode"`
	Enabled    bool   `json:"enabled"`
	Container  bool   `json:"container"`
	FeedURL    string `json:"feedUrl"`
	Window     string `json:"window"`
	Interval   string `json:"interval"`
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Available  bool   `json:"available"`
	Notes      string `json:"notes"`
	Checked    string `json:"checked"` // RFC 3339, empty before the first check
	Error      string `json:"error"`   // in the request language
	Blocked    string `json:"blocked"` // selfupdate.Blocked* code, empty when an automatic install would run
	Installed  string `json:"installed"`
	Restarting bool   `json:"restarting"`
}

// toUpdateStatus renders st for the request.
func toUpdateStatus(r *http.Request, st selfupdate.Status) updateStatus {
	out := updateStatus{
		Mode:       string(st.Mode),
		Enabled:    st.Enabled,
		Container:  st.Container,
		FeedURL:    st.FeedURL,
		Window:     st.Window,
		Current:    st.Current,
		Latest:     st.Latest,
		Available:  st.Available,
		Notes:      st.Notes,
		Error:      i18n.ErrorIn(i18n.FromRequest(r), st.Err),
		Blocked:    st.Blocked,
		Installed:  st.Installed,
		Restarting: st.Restarting,
	}
	if st.Interval > 0 {
		out.Interval = st.Interval.String()
	}
	if !st.Checked.IsZero() {
		out.Checked = st.Checked.Format(time.RFC3339)
	}
	return out
}

// handleUpdateStatus: GET /api/update.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request, _ *model.User) {
	writeJSON(w, r, http.StatusOK, toUpdateStatus(r, s.updates.Status()))
}

// handleUpdateCheck: POST /api/update/check - queries the release feed now.
// Feed errors are part of the returned status.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request, _ *model.User) {
	if !s.updatesEnabled(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	st, _ := s.updates.Check(ctx)
	writeJSON(w, r, http.StatusOK, toUpdateStatus(r, st))
}

// handleUpdateInstall: POST /api/update/install - installs a newer release on
// the admin's request. As a service the process restarts on its own shortly
// after the response; the card then waits for the new version.
func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !s.updatesEnabled(w, r) {
		return
	}
	before := s.updates.Status()
	slog.Info("Software update requested", "component", "update", "user", u.Username, "remote", clientAddr(r), "current", before.Current)
	// Detached from the request: an interrupted download must not leave the
	// install half-done because the browser went away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	st, err := s.updates.Install(ctx)
	if err != nil {
		slog.Error("Software update failed", "component", "update", "user", u.Username, "error", err)
	} else if st.Installed != "" {
		slog.Info("Software update installed", "component", "update", "user", u.Username,
			"from", st.Current, "to", st.Installed, "restarting", st.Restarting)
	}
	writeJSON(w, r, http.StatusOK, toUpdateStatus(r, st))
}

// updatesEnabled answers 409 when the updater is off or runs in a container.
func (s *Server) updatesEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.updates == nil || !s.updates.Status().Enabled {
		writeError(w, r, http.StatusConflict, "err.update.off")
		return false
	}
	return true
}
