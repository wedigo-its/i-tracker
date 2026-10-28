// Package iperf führt iperf3-Tests aus, wertet deren JSON-Stream-Ausgabe aus
// und hält den Live-Status laufender Tests vor.
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

// liveRetention ist die Zeit, die der Live-Status nach Testende abrufbar
// bleibt, damit die Oberfläche die Endwerte anzeigen kann.
const liveRetention = 10 * time.Second

// LiveStatus ist der Zwischenstand eines wartenden oder laufenden Tests.
type LiveStatus struct {
	ServerID            int64
	Status              model.TestStatus
	Progress            int
	ElapsedSeconds      int
	TotalSeconds        int
	CurrentDownloadMbps float64
	CurrentUploadMbps   float64
}

// Runner führt Tests nacheinander aus – es läuft immer nur ein iperf3 gleichzeitig.
type Runner struct {
	store      *store.Store
	cmdMu      sync.RWMutex
	configured string   // fest konfigurierter Pfad; dann keine Installation
	command    []string // iperf3-Binary, ggf. mit festen Vorab-Argumenten (Tests)
	status     Status
	sem        chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	mu   sync.RWMutex
	live map[int64]*LiveStatus
}

// NewRunner erstellt einen Runner. command ist der iperf3-Aufruf, in der
// Regel nur der Pfad zur Binary.
func NewRunner(st *store.Store, command ...string) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		store:   st,
		command: command,
		status:  Status{Path: command[0]}, // geprüft wird erst in Prepare
		sem:     make(chan struct{}, 1),
		ctx:     ctx,
		cancel:  cancel,
		live:    map[int64]*LiveStatus{},
	}
}

// Submit reiht einen gespeicherten, wartenden Test zur Ausführung ein. Der
// zurückgegebene Kanal wird geschlossen, sobald das Ergebnis gespeichert ist.
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

// Stop bricht laufende und wartende Tests ab und wartet, bis deren Status gespeichert ist.
func (r *Runner) Stop() {
	r.cancel()
	r.wg.Wait()
}

// Live liefert den Live-Status eines Tests, solange er wartet, läuft oder
// gerade beendet wurde.
func (r *Runner) Live(id int64) (LiveStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.live[id]
	if !ok {
		return LiveStatus{}, false
	}
	return *l, true
}

// LiveAll liefert den Live-Status aller wartenden, laufenden und gerade
// beendeten Tests (Test-ID → Status).
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

// process wartet auf den Ausführungs-Slot, führt den Test aus und speichert das Ergebnis.
func (r *Runner) process(id int64) {
	// Datenbankzugriffe laufen bewusst ohne r.ctx, damit ein Abbruch beim
	// Beenden noch gespeichert wird.
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
		// Test wurde zwischenzeitlich gelöscht (oder DB-Fehler): nichts mehr zu tun.
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

// trackProgress aktualisiert Fortschritt und Laufzeit alle 500 ms. Die
// zurückgegebene Funktion beendet die Aktualisierung.
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

// Args baut die iperf3-Argumente für einen Test. Download nutzt -R (der
// Server sendet), Upload den Normalmodus (der Client sendet).
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
