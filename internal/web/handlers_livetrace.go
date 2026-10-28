package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
	"i-tracker/internal/trace"
)

// sseKeepAlive keeps proxies from dropping the stream during long timeout hops.
const sseKeepAlive = 15 * time.Second

// sseStream writes Server-Sent Events; safe for concurrent use.
type sseStream struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newSSEStream(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)
	return &sseStream{w: w, rc: http.NewResponseController(w)}
}

// send writes v as JSON in an unnamed data event.
func (s *sseStream) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.write("data: " + string(data) + "\n\n")
}

func (s *sseStream) ping() { s.write(": ping\n\n") }

func (s *sseStream) write(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(s.w, chunk)
	s.rc.Flush()
}

// handleLiveTrace streams a trace hop by hop: start → hop … →
// interpolation_complete → complete, or error. Auth errors are error events too,
// since EventSource cannot read the HTTP status. A client disconnect cancels
// the trace without saving it.
func (s *Server) handleLiveTrace(w http.ResponseWriter, r *http.Request) {
	destination := r.PathValue("destination")
	stream := newSSEStream(w)
	lang := i18n.FromRequest(r)
	// fail sends err as an error event; code is its catalog key, as in error responses.
	fail := func(err error) {
		stream.send(map[string]string{"type": "error", "message": i18n.ErrorIn(lang, err), "code": i18n.KeyOf(err)})
	}

	if _, _, err := s.userForRequest(r, false); err != nil {
		if !errors.Is(err, errUnauthorized) && !errors.Is(err, errInactive) && !errors.Is(err, errMustChangePass) {
			slog.Error("Live trace: sign-in could not be checked", "error", err)
			err = i18n.E("err.internal")
		}
		fail(err)
		return
	}
	if n := utf8.RuneCountInString(destination); n < 1 || n > 255 {
		fail(i18n.E("err.trace.destination_length"))
		return
	}

	ctx := r.Context()
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(sseKeepAlive)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				stream.ping()
			}
		}
	}()

	stream.send(map[string]string{"type": "start", "destination": destination})
	tr := s.tracer.Run(ctx, destination, trace.DefaultOptions(), func(h model.TraceHop) {
		stream.send(map[string]any{"type": "hop", "data": h})
	})

	if ctx.Err() != nil {
		slog.Info("Live trace cancelled by the client", "destination", destination)
		return
	}
	if len(tr.Hops) == 0 && tr.ErrorMessage != nil {
		stream.send(map[string]string{"type": "error", "message": i18n.Localize(lang, tr.ErrorText, *tr.ErrorMessage),
			"code": i18n.StoredKey(tr.ErrorText, *tr.ErrorMessage)})
		return
	}
	stream.send(map[string]any{"type": "interpolation_complete", "hops": tr.Hops})

	// Save before "complete": the UI then reloads the history.
	if err := s.store.CreateTrace(context.WithoutCancel(ctx), tr); err != nil {
		slog.Error("Live trace could not be saved", "destination", destination, "error", err)
		fail(i18n.E("err.trace.save_failed"))
		return
	}
	stream.send(map[string]any{"type": "complete", "trace_id": tr.ID})
}
