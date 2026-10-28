package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"i-tracker/internal/db"
	"i-tracker/internal/model"
)

const userColumns = `id, username, email, hashed_password, is_active, is_admin, created_at, last_login, must_change_password, token_version`

func scanUser(row rowScanner) (*model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.HashedPassword, &u.IsActive, &u.IsAdmin,
		db.Time(&u.CreatedAt), db.NullTime(&u.LastLogin), &u.MustChangePassword, &u.TokenVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UserByID loads a user by ID.
func (s *Store) UserByID(ctx context.Context, id int64) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(noCancel(ctx), `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserByUsername loads a user by username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(noCancel(ctx), `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// UserByEmail loads a user by email address.
func (s *Store) UserByEmail(ctx context.Context, email string) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(noCancel(ctx), `SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

// ListUsers returns all users, ordered by ID.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(noCancel(ctx), `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// CountUsers returns the number of users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(noCancel(ctx), `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser stores u and sets ID and CreatedAt.
func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	u.CreatedAt = db.Now()
	res, err := s.db.ExecContext(noCancel(ctx),
		`INSERT INTO users (username, email, hashed_password, is_active, is_admin, created_at, must_change_password)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.Email, u.HashedPassword, u.IsActive, u.IsAdmin, db.FormatTime(u.CreatedAt), u.MustChangePassword)
	if err != nil {
		return err
	}
	u.ID, err = res.LastInsertId()
	return err
}

// DeleteUser deletes the user with the given ID.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return affectedOne(s.db.ExecContext(noCancel(ctx), `DELETE FROM users WHERE id = ?`, id))
}

// SetLastLogin sets the last sign-in time.
func (s *Store) SetLastLogin(ctx context.Context, id int64, t time.Time) error {
	_, err := s.db.ExecContext(noCancel(ctx), `UPDATE users SET last_login = ? WHERE id = ?`, db.FormatTime(t), id)
	return err
}

// SetPassword replaces a user's password hash.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	return affectedOne(s.db.ExecContext(noCancel(ctx), `UPDATE users SET hashed_password = ? WHERE id = ?`, hash, id))
}

// ChangePassword sets a new password, clears the forced change and bumps the token version
// (ending all sessions); returns the new version.
func (s *Store) ChangePassword(ctx context.Context, id int64, hash string) (int64, error) {
	return scanVersion(s.db.QueryRowContext(noCancel(ctx),
		`UPDATE users SET hashed_password = ?, must_change_password = 0, token_version = token_version + 1
		 WHERE id = ? RETURNING token_version`, hash, id))
}

// BumpTokenVersion ends all of the user's sessions; returns the new token version.
func (s *Store) BumpTokenVersion(ctx context.Context, id int64) (int64, error) {
	return scanVersion(s.db.QueryRowContext(noCancel(ctx),
		`UPDATE users SET token_version = token_version + 1 WHERE id = ? RETURNING token_version`, id))
}

// scanVersion reads the token version from RETURNING.
func scanVersion(row *sql.Row) (int64, error) {
	var v int64
	if err := row.Scan(&v); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	return v, nil
}

// SetMustChangePassword sets or clears the forced password change at next sign-in.
func (s *Store) SetMustChangePassword(ctx context.Context, id int64, must bool) error {
	return affectedOne(s.db.ExecContext(noCancel(ctx), `UPDATE users SET must_change_password = ? WHERE id = ?`, must, id))
}
