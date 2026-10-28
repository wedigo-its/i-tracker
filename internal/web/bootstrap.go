package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"i-tracker/internal/auth"
	"i-tracker/internal/store"
)

// EnsureAdmin creates the first admin while no user exists, with a random
// password that is logged once and must be changed at first sign-in.
func EnsureAdmin(ctx context.Context, st *store.Store) (created bool, err error) {
	created, password, err := createFirstAdmin(ctx, st)
	if err != nil || !created {
		return created, err
	}
	slog.Warn("First admin created: note the password, it is shown only this once and must be changed at the first sign-in",
		"user", auth.DefaultAdminUsername, "initial_password", password)
	if InitialPasswordHook != nil {
		InitialPasswordHook(auth.DefaultAdminUsername, password)
	}
	return true, nil
}

// InitialPasswordHook runs after the first admin is created (e.g. to also write the password to a file as a service).
var InitialPasswordHook func(username, password string)

func createFirstAdmin(ctx context.Context, st *store.Store) (bool, string, error) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return false, "", fmt.Errorf("users could not be counted: %w", err)
	}
	if n > 0 {
		return false, "", nil
	}
	password := auth.GeneratePassword()
	admin, err := auth.NewAdmin(auth.DefaultAdminUsername, auth.DefaultAdminEmail, password, true)
	if err != nil {
		return false, "", err
	}
	if err := st.CreateUser(ctx, admin); err != nil {
		return false, "", fmt.Errorf("admin could not be created: %w", err)
	}
	return true, password, nil
}

// FlagLegacyDefaultPassword forces a password change if the admin still uses the old fixed default (admin123).
func FlagLegacyDefaultPassword(ctx context.Context, st *store.Store) error {
	u, err := st.UserByUsername(ctx, auth.DefaultAdminUsername)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.MustChangePassword || !auth.CheckPassword(u.HashedPassword, auth.LegacyDefaultPassword) {
		return nil
	}
	if err := st.SetMustChangePassword(ctx, u.ID, true); err != nil {
		return err
	}
	slog.Warn("Admin still uses the former default password, change forced at the next sign-in", "user", u.Username)
	return nil
}

// legacyPasswordCache caches the legacy-password check per hash so the public
// /api/auth/status runs Argon2id once per hash, not per request.
type legacyPasswordCache struct {
	mu     sync.Mutex
	hash   string
	legacy bool
}

func (c *legacyPasswordCache) isLegacy(hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hash != hash {
		c.hash, c.legacy = hash, auth.CheckPassword(hash, auth.LegacyDefaultPassword)
	}
	return c.legacy
}

// initialPasswordPending reports whether the admin still has the generated
// first-start password (change pending, but not due to the legacy default).
func (s *Server) initialPasswordPending(ctx context.Context) (bool, error) {
	u, err := s.store.UserByUsername(ctx, auth.DefaultAdminUsername)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return u.MustChangePassword && !s.legacyAdmin.isLegacy(u.HashedPassword), nil
}
