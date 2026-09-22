// Package logger provides structured request logging and trace IDs.
package logger

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config controls logger behaviour.
type Config struct {
	// Level is one of: debug, info, warn, error.
	Level string
	// Format is one of: json, text.
	Format string
	// Requests enables request access logging.
	Requests bool
}

// Defaults returns the default logger config.
func Defaults() Config {
	return Config{
		Level:    "info",
		Format:   "json",
		Requests: true,
	}
}

// New creates a structured slog.Logger based on the provided config.
func New(cfg Config, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	level := parseLevel(cfg.Level)
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Middleware wraps an http.Handler with access logging and trace IDs.
func Middleware(logger *slog.Logger, logRequests bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			traceID := extractOrGenerateTraceID(r)
			ww := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

			r = r.WithContext(WithTraceID(r.Context(), traceID))
			ww.Header().Set("X-Trace-Id", traceID)

			next.ServeHTTP(ww, r)

			if logRequests {
				logger.Info("request",
					slog.String("trace_id", traceID),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", ww.statusCode),
					slog.Duration("duration", time.Since(start)),
					slog.String("client_ip", clientIP(r)),
				)
			}
		})
	}
}

func extractOrGenerateTraceID(r *http.Request) string {
	for _, h := range []string{"X-Trace-Id", "X-Request-Id", "Request-Id"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return generateTraceID()
}

func generateTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	host, _, _ := strings.Cut(r.RemoteAddr, ":")
	return host
}

// responseRecorder captures the status code written by the handler.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rr *responseRecorder) WriteHeader(code int) {
	if !rr.written {
		rr.statusCode = code
		rr.written = true
		rr.ResponseWriter.WriteHeader(code)
	}
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.written {
		rr.WriteHeader(http.StatusOK)
	}
	return rr.ResponseWriter.Write(b)
}
