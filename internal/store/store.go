// Package store wraps all database access.
package store

import (
	"context"
	"database/sql"
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrNotFound means the requested record does not exist.
	ErrNotFound = errors.New("record not found")
	// ErrDuplicate means a unique value is already taken.
	ErrDuplicate = errors.New("value already taken")
)

// mapUnique maps a UNIQUE violation to ErrDuplicate.
func mapUnique(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrDuplicate
	}
	return err
}

// affectedOne returns ErrNotFound when no row was affected.
func affectedOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Store holds the database access for all entities.
type Store struct {
	db *sql.DB
}

// New creates a Store on an open database.
func New(conn *sql.DB) *Store { return &Store{db: conn} }

// noCancel detaches queries from caller cancellation: a cancel mid-query can orphan the
// connection, keeping the file open and blocking WAL checkpoints; local queries are short anyway.
func noCancel(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// rowScanner covers *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}
