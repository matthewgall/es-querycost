package logger

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	cfg := Defaults()
	cfg.Format = "text"
	logger := New(cfg, &buf)
	logger.Info("hello", "key", "value")
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected log output, got %s", buf.String())
	}
}

func TestMiddlewareLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	cfg := Defaults()
	cfg.Format = "json"
	logger := New(cfg, &buf)

	handler := Middleware(logger, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	if !strings.Contains(buf.String(), "request") {
		t.Errorf("expected request log, got %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"status":201`) && !strings.Contains(buf.String(), `"status": 201`) {
		t.Errorf("expected status in log, got %s", buf.String())
	}
}

func TestMiddlewarePropagatesTraceID(t *testing.T) {
	var seen string
	handler := Middleware(New(Defaults(), nil), false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TraceIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	req.Header.Set("X-Trace-Id", "abc-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if seen != "abc-123" {
		t.Errorf("expected trace id abc-123, got %s", seen)
	}
	if got := w.Header().Get("X-Trace-Id"); got != "abc-123" {
		t.Errorf("expected response header abc-123, got %s", got)
	}
}

func TestMiddlewareGeneratesTraceID(t *testing.T) {
	var seen string
	handler := Middleware(New(Defaults(), nil), false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TraceIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if seen == "" {
		t.Error("expected generated trace id")
	}
	if got := w.Header().Get("X-Trace-Id"); got != seen {
		t.Errorf("expected response header %s, got %s", seen, got)
	}
}
