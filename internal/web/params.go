package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"i-tracker/internal/i18n"
)

// pathID reads a numeric path parameter; on errors it answers 422 and ok is
// false.
func pathID(w http.ResponseWriter, r *http.Request, name string) (id int64, ok bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "err.param.invalid", name)
		return 0, false
	}
	return id, true
}

// queryInt reads an optional integer parameter in the range [lo, hi].
func queryInt(r *http.Request, name string, def, lo, hi int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, i18n.E("err.param.range", name, lo, hi)
	}
	return n, nil
}

// queryBool reads an optional boolean; nil means "not given". Accepts
// true/false, 1/0, yes/no, on/off.
func queryBool(r *http.Request, name string) (*bool, error) {
	v := strings.ToLower(r.URL.Query().Get(name))
	var b bool
	switch v {
	case "":
		return nil, nil
	case "true", "1", "yes", "on":
		b = true
	case "false", "0", "no", "off":
		b = false
	default:
		return nil, i18n.E("err.param.bool", name)
	}
	return &b, nil
}

// queryInt64 reads an optional integer parameter; nil = not given.
func queryInt64(r *http.Request, name string) (*int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, i18n.E("err.param.number", name)
	}
	return &n, nil
}

// queryTime reads an optional point in time (ISO 8601, with or without time
// zone, or a date only). Values without a time zone count as UTC.
func queryTime(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			t = t.UTC()
			return &t, nil
		}
	}
	return nil, i18n.E("err.param.time", name)
}

// paging reads skip (≥ 0) and limit (1–maxLimit, default defLimit).
func paging(r *http.Request, defLimit, maxLimit int) (skip, limit int, err error) {
	if skip, err = queryInt(r, "skip", 0, 0, 1<<31-1); err != nil {
		return 0, 0, err
	}
	limit, err = queryInt(r, "limit", defLimit, 1, maxLimit)
	return skip, limit, err
}
