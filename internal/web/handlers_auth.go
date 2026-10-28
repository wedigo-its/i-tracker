package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"i-tracker/internal/auth"
	"i-tracker/internal/db"
	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
	"i-tracker/internal/store"
)

// checkPassword prüft ein Passwort gegen einen Hash (in Tests ersetzbar).
var checkPassword = auth.CheckPassword

// tooManyAttempts antwortet mit 429, wenn die Client-Adresse gesperrt ist.
func (s *Server) tooManyAttempts(w http.ResponseWriter, r *http.Request, addr string) bool {
	if !s.limiter.Blocked(addr) {
		return false
	}
	slog.Warn("Sign-in blocked: too many failed attempts", "path", r.URL.Path, "remote", addr)
	w.Header().Set("Retry-After", strconv.Itoa(int(auth.LoginWindow/time.Second)))
	writeError(w, r, http.StatusTooManyRequests, "err.auth.too_many_attempts")
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	addr := clientAddr(r)
	if s.tooManyAttempts(w, r, addr) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	u, err := s.store.UserByUsername(r.Context(), req.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeInternal(w, r, err)
		return
	}
	// Auch bei unbekanntem Benutzer einen Argon2id-Hash prüfen, damit die
	// Antwortzeit nicht verrät, ob es das Konto gibt.
	hash := auth.DummyHash()
	if u != nil {
		hash = u.HashedPassword
	}
	if !checkPassword(hash, req.Password) || u == nil {
		s.limiter.Fail(addr)
		slog.Warn("Sign-in failed", "user", req.Username, "remote", addr)
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, r, http.StatusUnauthorized, "err.auth.wrong_credentials")
		return
	}
	if !u.IsActive {
		writeErr(w, r, http.StatusBadRequest, errInactive)
		return
	}
	// Ältere bcrypt-Hashes bei erfolgreicher Anmeldung transparent auf Argon2id umstellen.
	if auth.NeedsRehash(u.HashedPassword) {
		if hash, err := auth.HashPassword(req.Password); err == nil {
			if err := s.store.SetPassword(r.Context(), u.ID, hash); err != nil {
				slog.Warn("Password hash could not be updated", "user", u.Username, "error", err)
			} else {
				slog.Info("Password hash upgraded to Argon2id", "user", u.Username)
			}
		}
	}

	now := db.Now()
	if err := s.store.SetLastLogin(r.Context(), u.ID, now); err != nil {
		writeInternal(w, r, err)
		return
	}
	u.LastLogin = &now

	token, claims, err := s.tokens.Create(u.Username, u.TokenVersion)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Die Oberfläche nutzt das HttpOnly-Cookie; access_token bleibt für API-Clients
	// (Authorization: Bearer) erhalten.
	setSessionCookie(w, r, token, s.tokens.TTL())
	writeJSON(w, r, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"csrf_token":   s.tokens.CSRF(claims.SessionID),
		"user":         u,
	})
}

// handleLogout beendet die Sitzung der Anfrage (Cookie oder Bearer-Token):
// Ihre Kennung kommt bis zum spätestmöglichen Ablauf auf die Sperrliste, das
// Cookie wird gelöscht. Ohne gültiges Token wird nur das Cookie gelöscht.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, _ := sessionToken(r); token != "" {
		if claims, err := s.tokens.Parse(token); err == nil {
			// Verlängerte Tokens derselben Sitzung laufen spätestens jetzt + TTL ab.
			if !s.revoked.Revoke(claims.SessionID, time.Now().Add(s.tokens.TTL())) {
				// Sperrliste voll: ersatzweise alle Sitzungen des Benutzers beenden.
				if u, err := s.store.UserByUsername(r.Context(), claims.Username); err == nil {
					if _, err := s.store.BumpTokenVersion(r.Context(), u.ID); err != nil {
						writeInternal(w, r, err)
						return
					}
				}
			}
		}
	}
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleLogoutAll beendet alle Sitzungen des angemeldeten Benutzers auf allen
// Geräten, auch Bearer-Tokens (Token-Version erhöhen).
func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request, u *model.User) {
	if _, err := s.store.BumpTokenVersion(r.Context(), u.ID); err != nil {
		writeInternal(w, r, err)
		return
	}
	slog.Info("All sessions ended", "user", u.Username, "remote", clientAddr(r))
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthStatus liefert ohne Anmeldung den Zustand für die Anmeldeseite.
// initial_pending ist true, solange der Admin noch das beim ersten Start
// erzeugte Passwort hat; nur dann zeigt die Seite den Hinweis darauf.
// public_read meldet, ob Gäste die Leseansicht ohne Anmeldung sehen
// (auth.public_read); sonst zeigt die Oberfläche gleich die Anmeldung.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	pending, err := s.initialPasswordPending(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"auth_enabled": true, "initial_pending": pending, "public_read": s.publicRead})
}

// handleMe liefert den angemeldeten Benutzer und im Header X-CSRF-Token das
// CSRF-Token der Sitzung (nach einem Neuladen der Seite).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *model.User) {
	if token, _ := sessionToken(r); token != "" {
		if claims, err := s.tokens.Parse(token); err == nil {
			w.Header().Set(csrfHeader, s.tokens.CSRF(claims.SessionID))
		}
	}
	writeJSON(w, r, http.StatusOK, u)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, _ *model.User) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, users)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request, _ *model.User) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateNewUser(req.Username, req.Email, req.Password); err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, err)
		return
	}

	ctx := r.Context()
	if _, err := s.store.UserByUsername(ctx, req.Username); err == nil {
		writeError(w, r, http.StatusBadRequest, "err.auth.username_taken")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeInternal(w, r, err)
		return
	}
	if _, err := s.store.UserByEmail(ctx, req.Email); err == nil {
		writeError(w, r, http.StatusBadRequest, "err.auth.email_taken")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeInternal(w, r, err)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	u := &model.User{
		Username:       req.Username,
		Email:          req.Email,
		HashedPassword: hash,
		IsActive:       true,
		IsAdmin:        req.IsAdmin,
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, u)
}

// validateNewUser checks the field lengths.
func validateNewUser(username, email, password string) error {
	switch {
	case utf8.RuneCountInString(username) < 3 || utf8.RuneCountInString(username) > 50:
		return i18n.E("err.auth.username_length")
	case utf8.RuneCountInString(email) < 3 || utf8.RuneCountInString(email) > 100:
		return i18n.E("err.auth.email_length")
	}
	return validatePassword(password)
}

// validatePassword checks the length of a new password. Existing shorter
// passwords stay valid for signing in.
func validatePassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < auth.MinPasswordLength:
		return i18n.E("err.auth.password_short", auth.MinPasswordLength)
	case len(password) > auth.MaxPasswordBytes:
		return i18n.E("err.auth.password_long", auth.MaxPasswordBytes)
	}
	return nil
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, current *model.User) {
	id, ok := pathID(w, r, "user_id")
	if !ok {
		return
	}
	if id == current.ID {
		writeError(w, r, http.StatusBadRequest, "err.auth.delete_self")
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "err.auth.user_not_found")
		return
	} else if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]string{"message": message(r, "msg.auth.user_deleted")})
}

// handleChangePassword ändert das Passwort des angemeldeten Benutzers. Das
// aktuelle Passwort muss angegeben werden; ein falsches zählt als
// Fehlversuch. Danach ist ein erzwungener Wechsel erledigt und alle anderen
// Sitzungen des Benutzers sind beendet (Token-Version). Die eigene Sitzung
// läuft mit einem neuen Token weiter: als Cookie und für API-Clients als
// access_token in der Antwort.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, u *model.User) {
	addr := clientAddr(r)
	if s.tooManyAttempts(w, r, addr) {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !checkPassword(u.HashedPassword, req.CurrentPassword) {
		s.limiter.Fail(addr)
		slog.Warn("Password change: current password wrong", "user", u.Username, "remote", addr)
		writeError(w, r, http.StatusBadRequest, "err.auth.current_password_wrong")
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, err)
		return
	}
	if req.NewPassword == req.CurrentPassword {
		writeError(w, r, http.StatusUnprocessableEntity, "err.auth.password_same")
		return
	}
	// Die Sitzung wurde in requireUserPending bereits geprüft.
	token, fromCookie := sessionToken(r)
	claims, err := s.tokens.Parse(token)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err == nil {
		claims.Version, err = s.store.ChangePassword(r.Context(), u.ID, hash)
	}
	if err == nil {
		token, claims, err = s.tokens.Renew(claims)
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if fromCookie {
		setSessionCookie(w, r, token, s.tokens.TTL())
	}
	slog.Info("Password changed, other sessions ended", "user", u.Username, "remote", addr)
	writeJSON(w, r, http.StatusOK, map[string]string{
		"message":      message(r, "msg.auth.password_changed"),
		"access_token": token,
		"token_type":   "bearer",
		"csrf_token":   s.tokens.CSRF(claims.SessionID),
	})
}
