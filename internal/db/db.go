// Package db opens the SQLite database and creates the schema (pure-Go driver, no CGO).
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Open opens or creates the database at path, including its directory, and applies the schema.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("database directory could not be created: %w", err)
	}

	// PRAGMAs are per connection; via the DSN the driver sets them on every new one.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("database could not be opened: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite: serialize writes

	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("database schema could not be created: %w", err)
	}
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("database schema could not be updated: %w", err)
	}
	return conn, nil
}

// columnMigrations adds columns missing in older databases (CREATE TABLE IF NOT EXISTS leaves existing tables alone).
var columnMigrations = []struct{ table, column, ddl string }{
	{"users", "must_change_password", `ALTER TABLE users ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT 0`},
	{"users", "token_version", `ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0`},
	// Language-neutral form of error_message (i18n.Msg as JSON), since 2026.10.28.
	{"tests", "error_text", `ALTER TABLE tests ADD COLUMN error_text TEXT`},
	{"traces", "error_text", `ALTER TABLE traces ADD COLUMN error_text TEXT`},
}

func migrate(conn *sql.DB) error {
	for _, m := range columnMigrations {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, m.table, m.column).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := conn.Exec(m.ddl); err != nil {
			return fmt.Errorf("%s.%s: %w", m.table, m.column, err)
		}
	}
	return nil
}
