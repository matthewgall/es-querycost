package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"es-querycost/internal/config"
)

func TestOpenAPIEndpoint(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Errorf("response is not valid JSON: %v", err)
	}
	if doc["openapi"] != "3.0.3" {
		t.Errorf("unexpected openapi version: %v", doc["openapi"])
	}
}

func TestOpenAPIMethodNotAllowed(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
	assertJSONError(t, w.Body.Bytes(), "method_not_allowed")
}

func TestDocsEndpoint(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content-type, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/openapi.json") {
		t.Error("docs UI does not reference /openapi.json")
	}
	if !strings.Contains(body, "swagger-ui") {
		t.Error("docs UI does not contain swagger-ui")
	}
}

func TestDocsEndpointMethodNotAllowed(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
	assertJSONError(t, w.Body.Bytes(), "method_not_allowed")
}

func TestDocsSubPathNotFound(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
	assertJSONError(t, w.Body.Bytes(), "not_found")
}

func assertJSONError(t *testing.T, body []byte, wantCode string) {
	t.Helper()
	var got ErrorResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("error response is not valid JSON: %v", err)
	}
	if got.Error.Code != wantCode {
		t.Errorf("error code = %q, want %q", got.Error.Code, wantCode)
	}
	if got.Error.Message == "" {
		t.Error("error message is empty")
	}
}
