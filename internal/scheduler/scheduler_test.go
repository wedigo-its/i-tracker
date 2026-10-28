package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"i-tracker/internal/db"
	"i-tracker/internal/iperf"
	"i-tracker/internal/model"
	"i-tracker/internal/store"
	"i-tracker/internal/trace"
)

// A test "minute" is 40 ms; iperf3 and traceroute are missing on purpose, so runs fail fast but are stored.
const testMinute = 40 * time.Millisecond

type env struct {
	store *store.Store
	sched *Scheduler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close DB: %v", err)
		}
	})
	st := store.New(conn)

	runner := iperf.NewRunner(st, filepath.Join(t.TempDir(), "kein-iperf3"))
	t.Cleanup(runner.Stop)
	sched := New(st, runner, trace.NewTracer(nil, filepath.Join(t.TempDir(), "kein-traceroute")))
	sched.minute = testMinute
	t.Cleanup(sched.Stop)
	return &env{store: st, sched: sched}
}

func (e *env) server(t *testing.T, name string, mod func(*model.ServerSettings)) *model.Server {
	t.Helper()
	sv := &model.Server{ServerSettings: model.DefaultServerSettings()}
	sv.Name, sv.Host = name, "127.0.0.1"
	sv.ScheduleEnabled, sv.ScheduleIntervalMinutes = true, 1
	if mod != nil {
		mod(&sv.ServerSettings)
	}
	if err := e.store.CreateServer(context.Background(), sv); err != nil {
		t.Fatal(err)
	}
	return sv
}

func (e *env) tests(t *testing.T, sv *model.Server) []model.Test {
	t.Helper()
	list, err := e.store.ListTests(context.Background(), store.TestFilter{ServerID: &sv.ID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestSchedulerRunsDueServers(t *testing.T) {
	e := newEnv(t)
	plain := e.server(t, "plain", nil)
	udp := e.server(t, "udp", func(s *model.ServerSettings) {
		s.DefaultProtocol, s.DefaultDuration, s.DefaultParallel = model.ProtocolUDP, 7, 3
		s.DefaultUDPBandwidthMbps = new(float64)
		*s.DefaultUDPBandwidthMbps = 250
		s.AutoTraceEnabled = true
	})
	off := e.server(t, "ohne Zeitplan", func(s *model.ServerSettings) { s.ScheduleEnabled = false })
	disabled := e.server(t, "deaktiviert", func(s *model.ServerSettings) { s.Enabled = false })

	if err := e.sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.sched.Running() {
		t.Fatal("scheduler not running")
	}
	// The first run comes only after one interval.
	if n := len(e.tests(t, plain)); n != 0 {
		t.Errorf("immediate run after start: %d tests", n)
	}

	time.Sleep(6 * testMinute)
	if n := len(e.tests(t, plain)); n < 2 {
		t.Errorf("plain: %d tests after 6 intervals", n)
	}
	if n := len(e.tests(t, off)) + len(e.tests(t, disabled)); n != 0 {
		t.Errorf("unscheduled servers ran: %d tests", n)
	}

	// Scheduled tests use the server defaults, including UDP bandwidth.
	ut := e.tests(t, udp)
	if len(ut) == 0 {
		t.Fatal("udp: no tests")
	}
	got := ut[len(ut)-1]
	if got.Protocol != model.ProtocolUDP || got.Duration != 7 || got.ParallelStreams != 3 ||
		got.UDPBandwidthMbps == nil || *got.UDPBandwidthMbps != 250 {
		t.Errorf("test parameters: %+v", got)
	}

	// Auto-trace: a trace linked to the test is stored afterwards.
	deadline := time.Now().Add(2 * time.Second)
	for {
		traces, err := e.store.TracesByTest(context.Background(), got.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(traces) == 1 && traces[0].DestinationHost == "127.0.0.1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no auto-trace for test %d", got.ID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSchedulerUpdateAndRemove(t *testing.T) {
	e := newEnv(t)
	sv := e.server(t, "a", nil)
	if err := e.sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Schedule off: no more tests.
	sv.ScheduleEnabled = false
	if e.sched.Update(sv) {
		t.Error("Update reports scheduled although schedule is off")
	}
	time.Sleep(4 * testMinute)
	if n := len(e.tests(t, sv)); n != 0 {
		t.Errorf("after switching off: %d tests", n)
	}

	// Switch on again, then remove.
	sv.ScheduleEnabled = true
	if !e.sched.Update(sv) {
		t.Fatal("Update does not schedule")
	}
	time.Sleep(4 * testMinute)
	e.sched.Remove(sv.ID)
	time.Sleep(testMinute) // let a pass in progress finish
	n := len(e.tests(t, sv))
	if n == 0 {
		t.Fatal("no tests after switching on")
	}
	time.Sleep(4 * testMinute)
	if m := len(e.tests(t, sv)); m != n {
		t.Errorf("more tests after Remove: %d -> %d", n, m)
	}
}

func TestSchedulerKeepsUnchangedInterval(t *testing.T) {
	e := newEnv(t)
	sv := e.server(t, "a", func(s *model.ServerSettings) { s.ScheduleIntervalMinutes = 3 })
	if err := e.sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Saving again with the same interval must not reset the countdown.
	for i := 0; i < 5; i++ {
		time.Sleep(testMinute)
		e.sched.Update(sv)
	}
	if n := len(e.tests(t, sv)); n == 0 {
		t.Error("Update reset the countdown")
	}
}

func TestSchedulerNotStarted(t *testing.T) {
	e := newEnv(t)
	sv := e.server(t, "a", nil)
	if e.sched.Update(sv) || e.sched.Running() {
		t.Error("scheduler not started must not schedule")
	}
	e.sched.Remove(sv.ID)
	e.sched.Stop()
}
