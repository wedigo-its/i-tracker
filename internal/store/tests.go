package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"i-tracker/internal/db"
	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
)

// testColumns omits raw_output; only the detail view needs it.
const testColumns = `id, server_id, protocol, direction, duration, parallel_streams, udp_bandwidth_mbps, status,
	started_at, completed_at,
	download_bandwidth_mbps, download_bytes, download_jitter_ms, download_packet_loss_percent,
	upload_bandwidth_mbps, upload_bytes, upload_jitter_ms, upload_packet_loss_percent,
	retransmits, cpu_percent, error_message, COALESCE(error_text, ''), created_at`

func scanTest(row rowScanner, extra ...any) (*model.Test, error) {
	var t model.Test
	r := &t.TestResult
	dest := append([]any{&t.ID, &t.ServerID, &t.Protocol, &t.Direction, &t.Duration, &t.ParallelStreams, &t.UDPBandwidthMbps, &t.Status,
		db.NullTime(&t.StartedAt), db.NullTime(&t.CompletedAt),
		&r.DownloadBandwidthMbps, &r.DownloadBytes, &r.DownloadJitterMs, &r.DownloadPacketLossPercent,
		&r.UploadBandwidthMbps, &r.UploadBytes, &r.UploadJitterMs, &r.UploadPacketLossPercent,
		&r.Retransmits, &r.CPUPercent, &t.ErrorMessage, &t.ErrorText, db.Time(&t.CreatedAt)}, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) queryTests(ctx context.Context, query string, args ...any) ([]model.Test, error) {
	rows, err := s.db.QueryContext(noCancel(ctx), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tests := []model.Test{}
	for rows.Next() {
		t, err := scanTest(rows)
		if err != nil {
			return nil, err
		}
		tests = append(tests, *t)
	}
	return tests, rows.Err()
}

// TestFilter narrows ListTests; nil fields do not filter.
type TestFilter struct {
	ServerID *int64
	Status   *model.TestStatus
	From, To *time.Time
	Skip     int
	Limit    int
}

// ListTests returns tests, newest first.
func (s *Store) ListTests(ctx context.Context, f TestFilter) ([]model.Test, error) {
	query := `SELECT ` + testColumns + ` FROM tests WHERE 1 = 1`
	args := []any{}
	if f.ServerID != nil {
		query += ` AND server_id = ?`
		args = append(args, *f.ServerID)
	}
	if f.Status != nil {
		query += ` AND status = ?`
		args = append(args, *f.Status)
	}
	if f.From != nil {
		query += ` AND created_at >= ?`
		args = append(args, db.FormatTime(*f.From))
	}
	if f.To != nil {
		query += ` AND created_at <= ?`
		args = append(args, db.FormatTime(*f.To))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Skip)
	return s.queryTests(ctx, query, args...)
}

// TestByID loads a test without raw output.
func (s *Store) TestByID(ctx context.Context, id int64) (*model.Test, error) {
	return scanTest(s.db.QueryRowContext(noCancel(ctx), `SELECT `+testColumns+` FROM tests WHERE id = ?`, id))
}

// TestDetailByID loads a test with its server profile and raw output.
func (s *Store) TestDetailByID(ctx context.Context, id int64) (*model.TestDetail, error) {
	var raw *string
	t, err := scanTest(s.db.QueryRowContext(noCancel(ctx), `SELECT `+testColumns+`, raw_output FROM tests WHERE id = ?`, id), &raw)
	if err != nil {
		return nil, err
	}
	sv, err := s.ServerByID(ctx, t.ServerID)
	if err != nil {
		return nil, err
	}
	return &model.TestDetail{Test: *t, Server: *sv, RawOutput: raw}, nil
}

// LatestCompletedTest returns a server's newest completed test.
func (s *Store) LatestCompletedTest(ctx context.Context, serverID int64) (*model.Test, error) {
	return scanTest(s.db.QueryRowContext(noCancel(ctx),
		`SELECT `+testColumns+` FROM tests WHERE server_id = ? AND status = ?
		 ORDER BY created_at DESC, id DESC LIMIT 1`, serverID, model.StatusCompleted))
}

// CreateTest stores t as pending and sets ID and CreatedAt.
func (s *Store) CreateTest(ctx context.Context, t *model.Test) error {
	t.Status = model.StatusPending
	t.CreatedAt = db.Now()
	res, err := s.db.ExecContext(noCancel(ctx),
		`INSERT INTO tests (server_id, protocol, direction, duration, parallel_streams, udp_bandwidth_mbps, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ServerID, t.Protocol, t.Direction, t.Duration, t.ParallelStreams, t.UDPBandwidthMbps, t.Status, db.FormatTime(t.CreatedAt))
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	return err
}

// StartTest marks a test as running.
func (s *Store) StartTest(ctx context.Context, id int64, startedAt time.Time) error {
	return affectedOne(s.db.ExecContext(noCancel(ctx),
		`UPDATE tests SET status = ?, started_at = ? WHERE id = ?`,
		model.StatusRunning, db.FormatTime(startedAt), id))
}

// failureColumns returns error_message (English text) and error_text (catalog message JSON);
// both NULL without failure, error_text also NULL for foreign text, as in CreateTrace.
func failureColumns(failure error) (msg, text *string) {
	if failure == nil {
		return nil, nil
	}
	m := failure.Error()
	return &m, nullIfEmpty(i18n.MsgOf(failure).Encode())
}

// FinishTest stores final status, results, failure (nil = none) and raw output.
func (s *Store) FinishTest(ctx context.Context, id int64, status model.TestStatus, completedAt time.Time,
	r model.TestResult, failure error, rawOutput *string) error {
	errMsg, errText := failureColumns(failure)
	return affectedOne(s.db.ExecContext(noCancel(ctx),
		`UPDATE tests SET status = ?, completed_at = ?,
			download_bandwidth_mbps = ?, download_bytes = ?, download_jitter_ms = ?, download_packet_loss_percent = ?,
			upload_bandwidth_mbps = ?, upload_bytes = ?, upload_jitter_ms = ?, upload_packet_loss_percent = ?,
			retransmits = ?, cpu_percent = ?, error_message = ?, error_text = ?, raw_output = ?
		 WHERE id = ?`,
		status, db.FormatTime(completedAt),
		r.DownloadBandwidthMbps, r.DownloadBytes, r.DownloadJitterMs, r.DownloadPacketLossPercent,
		r.UploadBandwidthMbps, r.UploadBytes, r.UploadJitterMs, r.UploadPacketLossPercent,
		r.Retransmits, r.CPUPercent, errMsg, errText, rawOutput, id))
}

// FailUnfinishedTests fails all pending/running tests with reason; called on start for leftovers of an interrupted run.
func (s *Store) FailUnfinishedTests(ctx context.Context, reason error) (int64, error) {
	errMsg, errText := failureColumns(reason)
	res, err := s.db.ExecContext(noCancel(ctx),
		`UPDATE tests SET status = ?, completed_at = ?, error_message = ?, error_text = ? WHERE status IN (?, ?)`,
		model.StatusFailed, db.FormatTime(db.Now()), errMsg, errText, model.StatusPending, model.StatusRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteTest deletes a test; its traces cascade.
func (s *Store) DeleteTest(ctx context.Context, id int64) error {
	return affectedOne(s.db.ExecContext(noCancel(ctx), `DELETE FROM tests WHERE id = ?`, id))
}
