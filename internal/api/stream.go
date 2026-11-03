package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"amazing/internal/history"
)

func backtestWantsStream(r *http.Request) bool {
	q := r.URL.Query().Get("stream")
	if q == "1" || strings.EqualFold(q, "true") {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "ndjson")
}

type ndjsonSink struct {
	w       http.ResponseWriter
	mu      sync.Mutex
	started bool
	last    time.Time
}

func (s *ndjsonSink) start() {
	if s.started {
		return
	}
	s.w.Header().Set("Content-Type", "application/x-ndjson")
	s.w.Header().Set("Cache-Control", "no-cache, no-transform")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
}

func (s *ndjsonSink) write(v any) {
	s.start()
	_ = json.NewEncoder(s.w).Encode(v)
	_ = http.NewResponseController(s.w).Flush()
}

func (s *ndjsonSink) progress(p history.Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if p.Percent < 100 && s.started && now.Sub(s.last) < 120*time.Millisecond {
		return
	}
	s.last = now
	s.write(struct {
		Type    string `json:"type"`
		Stage   string `json:"stage,omitempty"`
		Percent int    `json:"percent"`
		Message string `json:"message,omitempty"`
		Detail  string `json:"detail,omitempty"`
	}{"progress", p.Stage, p.Percent, p.Message, p.Detail})
}

func (s *ndjsonSink) result(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(struct {
		Type   string `json:"type"`
		Result any    `json:"result"`
	}{"result", v})
}

// fail writes an NDJSON error if the stream already started. Returns false so
// the caller can still use a normal JSON status response.
func (s *ndjsonSink) fail(code, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return false
	}
	s.write(struct {
		Type  string `json:"type"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Type: "error",
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{code, message},
	})
	return true
}
