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

// checkPassword is replaceable in tests.
var checkPassword = auth.CheckPassword

// tooManyAttempts answers 429 if the client address is blocked.
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
	// Hash even for unknown users so timing does not reveal whether the account exists.
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
	// Upgrade legacy bcrypt hashes to Argon2id on successful sign-in.
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
	// The UI uses the HttpOnly cookie; access_token is for Bearer API clients.
	setSessionCookie(w, r, token, s.tokens.TTL())
	writeJSON(w, r, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"csrf_token":   s.tokens.CSRF(claims.SessionID),
		"user":         u,
	})
}

// handleLogout revokes the request's session (cookie or Bearer) until its
// latest possible expiry and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, _ := sessionToken(r); token != "" {
		if claims, err := s.tokens.Parse(token); err == nil {
			// Renewed tokens of this session expire by now + TTL at the latest.
			if !s.revoked.Revoke(claims.SessionID, time.Now().Add(s.tokens.TTL())) {
				// Revocation list full: end all of the user's sessions instead.
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

// handleLogoutAll ends all sessions of the user, Bearer tokens included, by bumping the token version.
func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request, u *model.User) {
	if _, err := s.store.BumpTokenVersion(r.Context(), u.ID); err != nil {
		writeInternal(w, r, err)
		return
	}
	slog.Info("All sessions ended", "user", u.Username, "remote", clientAddr(r))
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthStatus serves the sign-in page state without authentication.
// initial_pending: admin still has the generated password (page shows a hint);
// public_read: guests get the read-only view (auth.public_read).
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	pending, err := s.initialPasswordPending(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"auth_enabled": true, "initial_pending": pending, "public_read": s.publicRead})
}

// handleMe returns the user and, in X-CSRF-Token, the session's CSRF token (needed after a page reload).
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

// handleChangePassword requires the current password (a wrong one counts as a
// failed attempt), ends all other sessions and renews the caller's token.
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
	// Session already validated by requireUserPending.
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
