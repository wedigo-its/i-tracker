package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Fixed-width UTC microseconds: sorts lexically, so SQL date filters can compare strings.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// Now is UTC truncated to stored precision, so API responses match before and after saving.
func Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// FormatTime formats t for storage.
func FormatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// FormatNullTime formats t for storage; nil yields NULL.
func FormatNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

// Time scans a NOT NULL time column.
func Time(dst *time.Time) sql.Scanner {
	return scanner(func(t *time.Time) {
		if t != nil {
			*dst = *t
		}
	})
}

// NullTime scans a nullable time column; NULL yields nil.
func NullTime(dst **time.Time) sql.Scanner {
	return scanner(func(t *time.Time) { *dst = t })
}

type scanner func(*time.Time)

func (set scanner) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		set(nil)
		return nil
	case time.Time:
		t := v.UTC()
		set(&t)
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("timestamp: unexpected type %T", src)
	}
	t, err := parseTime(s)
	if err != nil {
		return err
	}
	set(&t)
	return nil
}

// parseTime also accepts SQLite's CURRENT_TIMESTAMP.
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{timeLayout, time.RFC3339Nano, "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q not readable", s)
}
