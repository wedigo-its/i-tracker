// Package scheduler runs each server's test at its interval, followed by a trace if auto-trace is on.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"i-tracker/internal/iperf"
	"i-tracker/internal/model"
	"i-tracker/internal/store"
	"i-tracker/internal/trace"
)

// Scheduler keeps one schedule per server; without Start (disabled by config), Update and Remove do nothing.
type Scheduler struct {
	store  *store.Store
	runner *iperf.Runner
	tracer *trace.Tracer
	// minute is the unit of schedule_interval_minutes (shortened in tests).
	minute time.Duration

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	jobs    map[int64]*job
	running bool
}

type job struct {
	interval time.Duration
	cancel   context.CancelFunc
}

// New creates a scheduler that is not yet started.
func New(st *store.Store, runner *iperf.Runner, tracer *trace.Tracer) *Scheduler {
	return &Scheduler{store: st, runner: runner, tracer: tracer, minute: time.Minute, jobs: map[int64]*job{}}
}

// Start schedules all enabled servers with schedule on.
func (s *Scheduler) Start(ctx context.Context) error {
	servers, err := s.store.ListServers(ctx, nil, 0, 1<<31-1)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.running = true
	s.mu.Unlock()

	n := 0
	for i := range servers {
		if s.Update(&servers[i]) {
			n++
		}
	}
	slog.Info("Scheduler started", "scheduled_servers", n)
	return nil
}

// Stop ends all schedules and waits for running passes; the runner's own Stop aborts running tests.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.cancel()
	s.jobs = map[int64]*job{}
	s.mu.Unlock()
	s.wg.Wait()
}

// Running reports whether the scheduler is running.
func (s *Scheduler) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Update syncs a server's schedule with its settings, keeping an unchanged interval running;
// reports whether the server is scheduled.
func (s *Scheduler) Update(sv *model.Server) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false
	}
	if !sv.Enabled || !sv.ScheduleEnabled {
		s.removeLocked(sv.ID)
		return false
	}
	interval := time.Duration(sv.ScheduleIntervalMinutes) * s.minute
	if j, ok := s.jobs[sv.ID]; ok {
		if j.interval == interval {
			return true
		}
		j.cancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	id := sv.ID
	s.jobs[id] = &job{interval: interval, cancel: cancel}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(ctx, id, interval)
	}()
	slog.Info("Schedule active", "server", sv.Name, "interval", interval)
	return true
}

// Remove unschedules a server, e.g. after deletion.
func (s *Scheduler) Remove(serverID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(serverID)
}

func (s *Scheduler) removeLocked(serverID int64) {
	if j, ok := s.jobs[serverID]; ok {
		j.cancel()
		delete(s.jobs, serverID)
		slog.Info("Schedule removed", "server", serverID)
	}
}

// loop runs the server each interval; the first run after one interval, never overlapping.
func (s *Scheduler) loop(ctx context.Context, serverID int64, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce(ctx, serverID)
		}
	}
}

// runOnce runs a test with the server's defaults, waits for it, then traces if auto-trace is on.
func (s *Scheduler) runOnce(ctx context.Context, serverID int64) {
	sv, err := s.store.ServerByID(ctx, serverID)
	if err != nil || !sv.Enabled || !sv.ScheduleEnabled {
		return
	}
	test := model.NewTestFromDefaults(sv)
	if err := s.store.CreateTest(ctx, test); err != nil {
		slog.Error("Scheduled test could not be created", "server", sv.Name, "error", err)
		return
	}
	slog.Info("Scheduled test", "server", sv.Name, "test", test.ID)
	select {
	case <-s.runner.Submit(test):
	case <-ctx.Done():
		return
	}

	if !sv.AutoTraceEnabled {
		return
	}
	tr := s.tracer.Run(ctx, sv.Host, trace.DefaultOptions(), nil)
	if ctx.Err() != nil {
		return
	}
	tr.TestID = &test.ID
	if err := s.store.CreateTrace(ctx, tr); err != nil {
		slog.Error("Auto trace could not be saved", "server", sv.Name, "error", err)
		return
	}
	slog.Info("Auto trace completed", "server", sv.Name, "test", test.ID, "hops", tr.TotalHops)
}
