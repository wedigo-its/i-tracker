package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"i-tracker/internal/model"
	"i-tracker/internal/trace"
)

// TestLiveTraceHelper fakes traceroute for TestLiveTrace: three hops ending at
// 127.0.0.1, then a line that must not be read.
func TestLiveTraceHelper(t *testing.T) {
	if os.Getenv("LIVE_TRACE_HELPER") != "1" {
		return
	}
	for _, line := range []string{
		"traceroute to 127.0.0.1 (127.0.0.1), 30 hops max",
		" 1  10.0.0.1  1.0 ms  1.0 ms  1.0 ms",
		" 2  * * *",
		" 3  127.0.0.1  2.0 ms  2.0 ms  2.0 ms",
		" 4  10.9.9.9  9.0 ms",
	} {
		fmt.Println(line)
		time.Sleep(20 * time.Millisecond)
	}
	os.Exit(0)
}

type sseEvent struct {
	Type        string           `json:"type"`
	Destination string           `json:"destination"`
	Message     string           `json:"message"`
	Code        *string          `json:"code"`
	Data        model.TraceHop   `json:"data"`
	Hops        []model.TraceHop `json:"hops"`
	TraceID     int64            `json:"trace_id"`
}

// readSSE reads all data events until the server closes the connection.
func readSSE(t *testing.T, srv *httptest.Server, destination, token string) []sseEvent {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+"/api/live-trace/stream/"+url.PathEscape(destination), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var events []sseEvent
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("unreadable event: %s", data)
		}
		events = append(events, ev)
	}
	return events
}

func eventTypes(events []sseEvent) string {
	types := make([]string, len(events))
	for i, ev := range events {
		types[i] = ev.Type
	}
	return strings.Join(types, ",")
}

func TestLiveTrace(t *testing.T) {
	t.Setenv("LIVE_TRACE_HELPER", "1")
	srv := newTestServerWithTracer(t, trace.NewTracer(nil, os.Args[0], "-test.run=^TestLiveTraceHelper$", "--"))
	tok := login(t, srv, "admin", "admin123")

	events := readSSE(t, srv, "127.0.0.1", tok)
	if got := eventTypes(events); got != "start,hop,hop,hop,interpolation_complete,complete" {
		t.Fatalf("events: %s", got)
	}
	if events[0].Destination != "127.0.0.1" {
		t.Errorf("start: %+v", events[0])
	}
	if h := events[3].Data; h.HopNumber != 3 || h.IPAddress == nil || *h.IPAddress != "127.0.0.1" {
		t.Errorf("last hop: %+v", h)
	}
	if len(events[4].Hops) != 3 || events[4].Hops[1].Responded {
		t.Errorf("interpolation_complete: %+v", events[4].Hops)
	}

	// Saved before "complete" is sent.
	var saved model.Trace
	if code := call(t, srv, "GET", "/api/traces/"+itoa(events[5].TraceID), tok, "", &saved); code != http.StatusOK {
		t.Fatalf("saved trace: %d", code)
	}
	if !saved.Completed || saved.TestID != nil || len(saved.Hops) != 3 || saved.DestinationHost != "127.0.0.1" {
		t.Errorf("saved trace: %+v", saved)
	}
}

func TestLiveTraceErrors(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")

	// Invalid token: error event instead of HTTP error (EventSource sees no status).
	if events := readSSE(t, srv, "127.0.0.1", "kaputt"); eventTypes(events) != "error" || events[0].Message == "" ||
		events[0].Code == nil || *events[0].Code != "err.auth.unauthorized" {
		t.Errorf("without valid token: %+v", events)
	}
	// No traceroute binary, no hops: start, then error with its catalog key; nothing saved.
	events := readSSE(t, srv, "127.0.0.1", tok)
	if eventTypes(events) != "start,error" || !strings.Contains(events[1].Message, "nicht gefunden") {
		t.Errorf("without tool: %+v", events)
	} else if events[1].Code == nil || *events[1].Code != "err.trace.tool_missing" {
		t.Errorf("without tool: code %v, want err.trace.tool_missing", events[1].Code)
	}
	var list []model.Trace
	if call(t, srv, "GET", "/api/traces", tok, "", &list); len(list) != 0 {
		t.Errorf("failed live trace was saved: %d", len(list))
	}
}
