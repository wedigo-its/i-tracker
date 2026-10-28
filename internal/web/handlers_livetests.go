package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"i-tracker/internal/model"
)

// liveTestsInterval is the live status poll interval; events go out only on change.
const liveTestsInterval = 500 * time.Millisecond

// liveTest is a live stream entry (fields as in GET /api/tests/{id}/live).
type liveTest struct {
	TestID              int64            `json:"test_id"`
	ServerID            int64            `json:"server_id"`
	ServerName          string           `json:"server_name"`
	IsRunning           bool             `json:"is_running"`
	Status              model.TestStatus `json:"status"`
	Progress            int              `json:"progress"`
	ElapsedSeconds      int              `json:"elapsed_seconds"`
	TotalSeconds        int              `json:"total_seconds"`
	CurrentDownloadMbps float64          `json:"current_download_mbps"`
	CurrentUploadMbps   float64          `json:"current_upload_mbps"`
}

// handleLiveTestsStream sends {"type":"live","tests":[…]} via SSE on connect and
// on every change; finished tests stay listed for 10 s with their final values.
func (s *Server) handleLiveTestsStream(w http.ResponseWriter, r *http.Request, _ *model.User) {
	stream := newSSEStream(w)
	ctx := r.Context()
	names := map[int64]string{}

	var last []byte
	send := func() {
		data, err := json.Marshal(map[string]any{"type": "live", "tests": s.liveTests(ctx, names)})
		if err != nil || string(data) == string(last) {
			return
		}
		last = data
		stream.write("data: " + string(data) + "\n\n")
	}
	send()

	tick := time.NewTicker(liveTestsInterval)
	defer tick.Stop()
	ping := time.NewTicker(sseKeepAlive)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			send()
		case <-ping.C:
			stream.ping()
		}
	}
}

// liveTests builds the live stream list; names caches server names.
func (s *Server) liveTests(ctx context.Context, names map[int64]string) []liveTest {
	all := s.runner.LiveAll()
	out := make([]liveTest, 0, len(all))
	for id, l := range all {
		name, ok := names[l.ServerID]
		if !ok {
			if sv, err := s.store.ServerByID(ctx, l.ServerID); err == nil {
				name = sv.Name
				names[l.ServerID] = name
			}
		}
		out = append(out, liveTest{
			TestID: id, ServerID: l.ServerID, ServerName: name, IsRunning: true,
			Status: l.Status, Progress: l.Progress, ElapsedSeconds: l.ElapsedSeconds, TotalSeconds: l.TotalSeconds,
			CurrentDownloadMbps: l.CurrentDownloadMbps, CurrentUploadMbps: l.CurrentUploadMbps,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TestID < out[j].TestID })
	return out
}
