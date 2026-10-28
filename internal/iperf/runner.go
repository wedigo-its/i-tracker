// Package iperf runs iperf3 tests, parses their JSON stream output and keeps
// the live status of running tests.
package iperf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"i-tracker/internal/db"
	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
	"i-tracker/internal/store"
)

// liveRetention keeps the live status after a test ends so the UI can show the
// final values.
const liveRetention = 10 * time.Second

// LiveStatus is the interim state of a pending or running test.
type LiveStatus struct {
	ServerID            int64
	Status              model.TestStatus
	Progress            int
	ElapsedSeconds      int
	TotalSeconds        int
	CurrentDownloadMbps float64
	CurrentUploadMbps   float64
}

// Runner runs tests one at a time; only one iperf3 runs at once.
type Runner struct {
	store      *store.Store
	cmdMu      sync.RWMutex
	configured string   // fixed configured path; disables installation
	command    []string // iperf3 binary, optionally with fixed leading args (tests)
	status     Status
	sem        chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	mu   sync.RWMutex
	live map[int64]*LiveStatus
}

// NewRunner creates a Runner; command is the iperf3 call, usually just the
// binary path.
func NewRunner(st *store.Store, command ...string) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		store:   st,
		command: command,
		status:  Status{Path: command[0]}, // checked in Prepare
		sem:     make(chan struct{}, 1),
		ctx:     ctx,
		cancel:  cancel,
		live:    map[int64]*LiveStatus{},
	}
}

// Submit queues a stored pending test; the returned channel closes once the
// result is saved.
func (r *Runner) Submit(t *model.Test) <-chan struct{} {
	r.setLive(t.ID, &LiveStatus{ServerID: t.ServerID, Status: model.StatusPending, TotalSeconds: t.Duration})
	done := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(done)
		r.process(t.ID)
	}()
	return done
}

// Stop aborts running and pending tests and waits until their status is saved.
func (r *Runner) Stop() {
	r.cancel()
	r.wg.Wait()
}

// Live returns a test's live status while pending, running or just finished.
func (r *Runner) Live(id int64) (LiveStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.live[id]
	if !ok {
		return LiveStatus{}, false
	}
	return *l, true
}

// LiveAll returns the live status of all pending, running and just finished
// tests, keyed by test ID.
func (r *Runner) LiveAll() map[int64]LiveStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[int64]LiveStatus, len(r.live))
	for id, l := range r.live {
		out[id] = *l
	}
	return out
}

func (r *Runner) setLive(id int64, l *LiveStatus) {
	r.mu.Lock()
	r.live[id] = l
	r.mu.Unlock()
}

func (r *Runner) updateLive(id int64, f func(*LiveStatus)) {
	r.mu.Lock()
	if l, ok := r.live[id]; ok {
		f(l)
	}
	r.mu.Unlock()
}

func (r *Runner) deleteLive(id int64) {
	r.mu.Lock()
	delete(r.live, id)
	r.mu.Unlock()
}

// process waits for the run slot, runs the test and saves the result.
func (r *Runner) process(id int64) {
	// DB access deliberately skips r.ctx so an abort on shutdown is still saved.
	ctx := context.Background()

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-r.ctx.Done():
		r.finish(ctx, id, nil, model.TestResult{}, i18n.E("err.test.aborted_stop"), "")
		return
	}

	t, err := r.store.TestByID(ctx, id)
	if err != nil {
		// Test deleted meanwhile (or DB error): nothing left to do.
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("Test could not be loaded", "test", id, "error", err)
		}
		r.deleteLive(id)
		return
	}
	sv, err := r.store.ServerByID(ctx, t.ServerID)
	if err != nil {
		r.finish(ctx, id, t, model.TestResult{}, i18n.E("err.server.not_found"), "")
		return
	}

	if err := r.store.StartTest(ctx, id, db.Now()); err != nil {
		slog.Error("Test could not be marked as running", "test", id, "error", err)
	}
	r.updateLive(id, func(l *LiveStatus) { l.Status = model.StatusRunning })
	slog.Info("Starting iperf3 test", "test", id, "server", sv.Name, "direction", t.Direction, "protocol", t.Protocol)

	res, failure, raw := r.execute(t, sv)
	r.finish(ctx, id, t, res, failure, raw)
}

// finish stores the result (failure nil = success), sets the live status to
// the final state and removes it after liveRetention.
func (r *Runner) finish(ctx context.Context, id int64, t *model.Test, res model.TestResult, failure error, raw string) {
	status := model.StatusCompleted
	var rawPtr *string
	if failure != nil {
		status = model.StatusFailed
		slog.Warn("iperf3 test failed", "test", id, "error", failure)
	} else {
		slog.Info("iperf3 test completed", "test", id)
	}
	if raw != "" {
		rawPtr = &raw
	}
	if err := r.store.FinishTest(ctx, id, status, db.Now(), res, failure, rawPtr); err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("Test result could not be saved", "test", id, "error", err)
	}

	r.updateLive(id, func(l *LiveStatus) {
		l.Status = status
		if status == model.StatusCompleted {
			l.Progress = 100
			if t != nil {
				l.ElapsedSeconds = t.Duration
			}
			if res.DownloadBandwidthMbps != nil {
				l.CurrentDownloadMbps = *res.DownloadBandwidthMbps
			}
			if res.UploadBandwidthMbps != nil {
				l.CurrentUploadMbps = *res.UploadBandwidthMbps
			}
		}
	})
	time.AfterFunc(liveRetention, func() { r.deleteLive(id) })
}

// execute runs iperf3 and returns the results, the reason of a failure (nil
// on success) and the raw output.
func (r *Runner) execute(t *model.Test, sv *model.Server) (res model.TestResult, failure error, raw string) {
	timeout := time.Duration(t.Duration)*time.Second + 30*time.Second
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()

	command := r.commandLine()
	args := append(append([]string{}, command[1:]...), Args(sv.Host, sv.Port, t)...)
	cmd := exec.CommandContext(ctx, command[0], args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, i18n.Wrap(err, "err.iperf.start"), ""
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return res, i18n.E("err.iperf.not_found"), ""
		}
		return res, i18n.Wrap(err, "err.iperf.start"), ""
	}

	stopProgress := r.trackProgress(t.ID, t.Duration)
	defer stopProgress()

	var out strings.Builder
	var end *endData
	var iperfErr string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		out.Write(line)
		out.WriteByte('\n')

		var ev event
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Event {
		case "interval":
			var d intervalData
			if json.Unmarshal(ev.Data, &d) == nil {
				rates := intervalRates(d, t.Direction)
				r.updateLive(t.ID, func(l *LiveStatus) {
					if rates.DownloadMbps != nil {
						l.CurrentDownloadMbps = *rates.DownloadMbps
					}
					if rates.UploadMbps != nil {
						l.CurrentUploadMbps = *rates.UploadMbps
					}
				})
			}
		case "end":
			var d endData
			if json.Unmarshal(ev.Data, &d) == nil {
				end = &d
			}
		case "error":
			json.Unmarshal(ev.Data, &iperfErr)
		}
	}
	waitErr := cmd.Wait()

	raw = out.String()
	if raw == "" {
		raw = stderr.String()
	}
	switch {
	case r.ctx.Err() != nil:
		return res, i18n.E("err.test.aborted_stop"), raw
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return res, i18n.E("err.timeout", timeout), raw
	case iperfErr != "":
		return res, i18n.Foreign(iperfErr), raw
	case waitErr != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return res, i18n.Foreign(msg), raw
		}
		return res, i18n.Wrap(waitErr, "err.iperf.exit"), raw
	case end == nil:
		return res, i18n.E("err.iperf.no_results"), raw
	}
	res, ok := endResult(*end, t.Direction)
	if !ok {
		return res, i18n.E("err.iperf.no_results_direction", t.Direction), raw
	}
	return res, nil, raw
}

// trackProgress updates progress and elapsed time every 500 ms until stop is called.
func (r *Runner) trackProgress(id int64, duration int) (stop func()) {
	start := time.Now()
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				elapsed := time.Since(start).Seconds()
				r.updateLive(id, func(l *LiveStatus) {
					l.ElapsedSeconds = int(elapsed)
					l.Progress = min(100, int(elapsed/float64(duration)*100))
				})
			}
		}
	}()
	return func() { close(done) }
}

// Args builds the iperf3 arguments for a test. Download uses -R (server
// sends), upload the normal mode (client sends).
func Args(host string, port int, t *model.Test) []string {
	args := []string{
		"-c", host,
		"-p", strconv.Itoa(port),
		"-t", strconv.Itoa(t.Duration),
		"-P", strconv.Itoa(t.ParallelStreams),
		"-i", "1",
		"--json-stream",
		"--forceflush",
	}
	if t.Protocol == model.ProtocolUDP {
		args = append(args, "-u")
		if t.UDPBandwidthMbps != nil {
			args = append(args, "-b", strconv.FormatFloat(*t.UDPBandwidthMbps, 'f', -1, 64)+"M")
		}
	}
	switch t.Direction {
	case model.DirectionDownload:
		args = append(args, "-R")
	case model.DirectionBidirectional:
		args = append(args, "--bidir")
	}
	return args
}
