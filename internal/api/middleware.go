package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ctxKey int

const reqIDKey ctxKey = 1

func RequestID(r *http.Request) string {
	if v, ok := r.Context().Value(reqIDKey).(string); ok {
		return v
	}
	return ""
}

func withRequestID(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), reqIDKey, id)
		ww := &statusWriter{ResponseWriter: w, code: 200}
		start := time.Now()
		next.ServeHTTP(ww, r.WithContext(ctx))
		log.Info("http",
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.code,
			"ms", time.Since(start).Milliseconds(),
			"ip", clientIP(r),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	w.code = c
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack not supported")
	}
	return h.Hijack()
}

func cors(origins []string) func(http.Handler) http.Handler {
	allow := map[string]bool{}
	star := false
	for _, o := range origins {
		if o == "*" {
			star = true
		}
		allow[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (star || allow[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-Id")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ipLimiter is a crude token bucket per IP for the expensive backtest endpoint.
type ipLimiter struct {
	mu      sync.Mutex
	per     time.Duration
	burst   int
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(per time.Duration, burst int) *ipLimiter {
	return &ipLimiter{per: per, burst: burst, buckets: map[string]*bucket{}}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[ip]
	if b == nil {
		b = &bucket{tokens: float64(l.burst), last: now}
		l.buckets[ip] = b
	}
	elapsed := now.Sub(b.last).Seconds() / l.per.Seconds()
	b.tokens += elapsed
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
