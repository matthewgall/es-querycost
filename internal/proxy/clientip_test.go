package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustNetworks(t *testing.T, cidrs []string) []*net.IPNet {
	t.Helper()
	n, err := parseTrustedNetworks(cidrs)
	if err != nil {
		t.Fatalf("parse networks: %v", err)
	}
	return n
}

func TestResolveClientIPWithoutTrustedProxies(t *testing.T) {
	trusted := mustNetworks(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := resolveClientIP(req, trusted); got != "192.0.2.1" {
		t.Fatalf("expected remote addr, got %q", got)
	}
}

func TestResolveClientIPWithTrustedProxy(t *testing.T) {
	trusted := mustNetworks(t, []string{"127.0.0.1/8"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1, 127.0.0.2")

	if got := resolveClientIP(req, trusted); got != "10.0.0.1" {
		t.Fatalf("expected closest untrusted client 10.0.0.1, got %q", got)
	}
}

func TestResolveClientIPTrustedProxySpoofsIgnored(t *testing.T) {
	trusted := mustNetworks(t, []string{"10.0.0.0/8"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "10.0.0.99, 10.0.0.100")

	if got := resolveClientIP(req, trusted); got != "10.0.0.5" {
		t.Fatalf("expected peer address when all forwarded IPs are trusted, got %q", got)
	}
}

func TestResolveClientIPUsesXRealIpWhenTrusted(t *testing.T) {
	trusted := mustNetworks(t, []string{"192.168.1.0/24"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.10:1234"
	req.Header.Set("X-Real-Ip", "8.8.8.8")

	if got := resolveClientIP(req, trusted); got != "8.8.8.8" {
		t.Fatalf("expected X-Real-Ip value, got %q", got)
	}
}

func TestSanitizeClientIPMiddleware(t *testing.T) {
	var seen http.Header
	handler := sanitizeClientIP(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-Ip", "1.2.3.4")
	req.Header.Set("X-Custom", "keep")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Get("X-Forwarded-For"); got != "192.0.2.1" {
		t.Fatalf("expected sanitized X-Forwarded-For, got %q", got)
	}
	if seen.Get("X-Real-Ip") != "" {
		t.Fatalf("X-Real-Ip should have been stripped")
	}
	if seen.Get("X-Custom") != "keep" {
		t.Fatalf("custom header should be preserved")
	}
}

func TestSanitizeClientIPMiddlewareTrustedProxy(t *testing.T) {
	var seen http.Header
	handler := sanitizeClientIP([]string{"127.0.0.1/32"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 127.0.0.1")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := seen.Get("X-Forwarded-For"); got != "1.2.3.4" {
		t.Fatalf("expected original client IP from trusted proxy, got %q", got)
	}
}
