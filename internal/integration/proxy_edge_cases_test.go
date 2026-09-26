//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"es-querycost/internal/config"
	"es-querycost/internal/logger"
	"es-querycost/internal/proxy"
	"github.com/golang-jwt/jwt/v5"
)

func newTestServer(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	return server.Handler()
}

func postSearch(t *testing.T, handler http.Handler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func postSearchWithContentType(t *testing.T, handler http.Handler, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return postSearch(t, handler, body, headers)
}

// Malformed / weird request bodies

func TestProxyOversizedBody(t *testing.T) {
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = "http://localhost:9200"
	handler := newTestServer(t, cfg)

	body := append([]byte(`{"query":"`), bytes.Repeat([]byte("x"), 1<<20+100)...)
	body = append(body, []byte(`","index":"i"}`)...)

	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyNonJSONBody(t *testing.T) {
	cfg := config.Defaults()
	handler := newTestServer(t, cfg)

	cases := []struct {
		name string
		body string
	}{
		{"plain text", "this is not json"},
		{"xml", "<query>hello</query>"},
		{"binary-ish", "{not really json\x00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postSearch(t, handler, []byte(tc.body), nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for non-JSON body, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyQueryAsWrongType(t *testing.T) {
	cfg := config.Defaults()
	handler := newTestServer(t, cfg)

	cases := []struct {
		name string
		body string
	}{
		{"object", `{"query":{"match_all":{}},"index":"i"}`},
		{"number", `{"query":123,"index":"i"}`},
		{"boolean", `{"query":true,"index":"i"}`},
		{"array", `{"query":["a"],"index":"i"}`},
		{"null", `{"query":null,"index":"i"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postSearch(t, handler, []byte(tc.body), nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for wrong-typed query, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyEmptyQueryString(t *testing.T) {
	cfg := config.Defaults()
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"","index":"i"}`)
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty query, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyUnknownTopLevelField(t *testing.T) {
	cfg := config.Defaults()
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"asn:AS13335","index":"i","evil":"true"}`)
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown top-level field, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxySourceDisallowedField(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"asn:AS13335","index":"i","source":{"boost":2}}`)
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for disallowed source field, got %d: %s", w.Code, w.Body.String())
	}
}

// HTTP method and content-type oddities

func TestProxyMethodOddities(t *testing.T) {
	cfg := config.Defaults()
	handler := newTestServer(t, cfg)

	methods := []string{http.MethodOptions, http.MethodPatch, http.MethodHead, http.MethodTrace}
	paths := []string{"/search", "/validate", "/not-a-route"}

	for _, path := range paths {
		for _, method := range methods {
			t.Run(fmt.Sprintf("%s_%s", method, path), func(t *testing.T) {
				req := httptest.NewRequest(method, path, nil)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
					t.Fatalf("expected 405 or 404, got %d", w.Code)
				}
			})
		}
	}
}

func TestProxyContentTypeIgnored(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	handler := newTestServer(t, cfg)

	body, _ := json.Marshal(proxy.SearchRequest{
		Query: `asn:AS13335`,
		Index: "i",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	})

	cases := []struct {
		name string
		ct   string
	}{
		{"text/plain", "text/plain"},
		{"application/xml", "application/xml"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postSearchWithContentType(t, handler, body, tc.ct)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 regardless of content-type, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// API key auth edge cases

func apiKeyConfig(srvURL, key string) config.Config {
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srvURL
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		key: {UserID: "user-1", Plan: "free"},
	}
	return cfg
}

func TestProxyAPIKeyEdgeCases(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	body, _ := json.Marshal(proxy.SearchRequest{
		Query: `asn:AS13335`,
		Index: "i",
	})

	cases := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"wrong scheme", "Bearer correct-key"},
		{"empty key", "apikey "},
		{"whitespace", "apikey  "},
		{"wrong key", "apikey wrong-key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := apiKeyConfig(srv.URL, "correct-key")
			handler := newTestServer(t, cfg)
			headers := map[string]string{}
			if tc.header != "" {
				headers["Authorization"] = tc.header
			}
			w := postSearch(t, handler, body, headers)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for %q, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyMetricsWrongAPIKey(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"metrics-key": {UserID: "admin", Plan: "enterprise"},
	}
	handler := newTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "apikey wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for metrics with wrong key, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyJWTValidAndInvalid(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Auth.Type = "jwt"
	cfg.Auth.JWTSecret = "super-secret"
	handler := newTestServer(t, cfg)

	body, _ := json.Marshal(proxy.SearchRequest{
		Query: `asn:AS13335`,
		Index: "i",
	})

	valid := makeHMACToken(t, cfg.Auth.JWTSecret, map[string]any{"sub": "u1", "plan": "enterprise"})
	wrongSig := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwicGxhbiI6ImVudGVycHJpc2UiLCJpYXQiOjE2ODI4MjQ4MDB9.4AdcjSOP8pJ8CO7Y9x9z9eC4GkR2v7gjhvF1GJZd8wI"

	t.Run("valid", func(t *testing.T) {
		w := postSearch(t, handler, body, map[string]string{"Authorization": "Bearer " + valid})
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for valid JWT, got %d: %s", w.Code, w.Body.String())
		}
	})

	cases := []struct {
		name  string
		token string
	}{
		{"missing", ""},
		{"malformed", "not-a-token"},
		{"bad signature", wrongSig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var headers map[string]string
			if tc.token != "" {
				headers = map[string]string{"Authorization": "Bearer " + tc.token}
			}
			w := postSearch(t, handler, body, headers)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for %s, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

func makeHMACToken(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	importedClaims := jwt.MapClaims(claims)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, importedClaims)
	s, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// Lucene injection and weird query content

func TestProxyLuceneInjectionNotBypassingGate(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	handler := newTestServer(t, cfg)

	injections := []string{
		`*:*`,
		`asn:AS13335 OR *:*`,
		`title:*foo`,
		`@timestamp:[* TO now]`,
	}

	for _, q := range injections {
		t.Run(q, func(t *testing.T) {
			body, _ := json.Marshal(proxy.SearchRequest{
				Query: q,
				Index: "i",
				Context: map[string]any{
					"user": map[string]any{"plan": "free"},
				},
			})
			w := postSearch(t, handler, body, nil)
			if w.Code == http.StatusOK {
				t.Fatalf("injection query %q must not be allowed, got 200: %s", q, w.Body.String())
			}
		})
	}
}

func TestProxyUnicodeAndSpecialCharacters(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	handler := newTestServer(t, cfg)

	queries := []string{
		`asn:AS13335 AND msg:日本語`,
		`asn:AS13335 AND msg:"emoji 🚀"`,
	}

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			body, _ := json.Marshal(proxy.SearchRequest{
				Query: q,
				Index: "i",
				Context: map[string]any{
					"user": map[string]any{"plan": "enterprise"},
				},
			})
			w := postSearch(t, handler, body, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for unicode query, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyNullByteInQuery(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	handler := newTestServer(t, cfg)

	// JSON string can contain escaped null byte.
	body := []byte(`{"query":"asn:\u0000","index":"i","context":{"user":{"plan":"free"}}}`)
	w := postSearch(t, handler, body, nil)
	if w.Code == http.StatusInternalServerError {
		t.Fatalf("null byte crashed the proxy, got 500")
	}
}

func TestProxyBoundaryCostAllowed(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.CostModel.TermCost = 50
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"asn:AS13335","index":"i","context":{"user":{"plan":"free"}}}`)
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for query cost exactly at limit, got %d: %s", w.Code, w.Body.String())
	}
}

// Index-name abuse

func TestProxyWildcardIndex(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	seedDocument(t, index)

	cfg := esConfig(t)
	handler := newTestServer(t, cfg)

	body, _ := json.Marshal(proxy.SearchRequest{
		Query: `asn:AS13335`,
		Index: index[:len(index)-1] + `*`,
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	})
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for wildcard index, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"AS13335"`)) {
		t.Fatalf("expected seeded doc in wildcard index response, got: %s", w.Body.String())
	}
}

func TestProxySystemIndexForwardedButRejected(t *testing.T) {
	skipIfESUnreachable(t)
	cfg := esConfig(t)
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"*:*","index":"_cluster","context":{"user":{"plan":"enterprise"}}}`)
	w := postSearch(t, handler, body, nil)
	if w.Code == http.StatusOK {
		t.Fatalf("system index query should not succeed, got 200: %s", w.Body.String())
	}
}

func TestProxyCommaSeparatedIndex(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	seedDocument(t, index)

	// Create a second index so the comma-separated list is valid.
	other := ensureIndex(t)

	cfg := esConfig(t)
	handler := newTestServer(t, cfg)

	body := []byte(fmt.Sprintf(`{"query":"asn:AS13335","index":"%s,%s","context":{"user":{"plan":"free"}}}`, index, other))
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for comma-separated indices, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"AS13335"`)) {
		t.Fatalf("expected seeded doc in comma-separated index response, got: %s", w.Body.String())
	}
}

// Upstream resilience

func TestProxyUpstreamConnectionClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack not supported")
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.ProxyTimeout = "2s"
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"asn:AS13335","index":"i","context":{"user":{"plan":"free"}}}`)
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on abrupt close, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyUnresolvableUpstream(t *testing.T) {
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = "http://es-querycost.invalid:9200"
	cfg.ProxyTimeout = "5s"
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"asn:AS13335","index":"i","context":{"user":{"plan":"free"}}}`)
	start := time.Now()
	w := postSearch(t, handler, body, nil)
	elapsed := time.Since(start)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for unresolvable upstream, got %d: %s", w.Code, w.Body.String())
	}
	if elapsed > 10*time.Second {
		t.Fatalf("unresolvable upstream took too long: %v", elapsed)
	}
}

func TestProxyInvalidUpstreamScheme(t *testing.T) {
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = "ftp://localhost:9200"
	cfg.ProxyTimeout = "2s"
	handler := newTestServer(t, cfg)

	body := []byte(`{"query":"asn:AS13335","index":"i","context":{"user":{"plan":"free"}}}`)
	w := postSearch(t, handler, body, nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for invalid scheme, got %d: %s", w.Code, w.Body.String())
	}
}

// Header hygiene

func TestProxyStripsSensitiveHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		esSearchHandler()(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	handler := newTestServer(t, cfg)

	headers := map[string]string{
		"Authorization":       "Basic secret",
		"Cookie":              "session=abc",
		"Proxy-Authorization": "Basic proxy-secret",
		"X-Custom":            "keep-me",
	}
	body := []byte(`{"query":"asn:AS13335","index":"i","context":{"user":{"plan":"free"}}}`)
	w := postSearch(t, handler, body, headers)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if gotHeaders.Get(h) != "" {
			t.Fatalf("sensitive header %q leaked upstream: %q", h, gotHeaders.Get(h))
		}
	}
	if gotHeaders.Get("X-Custom") != "keep-me" {
		t.Fatalf("expected non-sensitive header to be preserved, got %q", gotHeaders.Get("X-Custom"))
	}
}

// Concurrency and load

func TestProxyConcurrentAllowedAndDenied(t *testing.T) {
	srv := httptest.NewServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"valid-key": {UserID: "u1", Plan: "free"},
	}
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	handler := server.Handler()

	cheap := []byte(`{"query":"asn:AS13335","index":"i"}`)
	expensive := []byte(`{"query":"*:*","index":"i"}`)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := cheap
			if n%2 == 0 {
				body = expensive
			}
			w := postSearch(t, handler, body, map[string]string{"Authorization": "apikey valid-key"})
			if n%2 == 0 && w.Code != http.StatusPaymentRequired {
				t.Errorf("expected 402 for expensive query, got %d", w.Code)
			}
			if n%2 == 1 && w.Code != http.StatusOK {
				t.Errorf("expected 200 for cheap query, got %d", w.Code)
			}
		}(i)
	}
	wg.Wait()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "apikey valid-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for metrics, got %d: %s", w.Code, w.Body.String())
	}
	output := w.Body.String()
	if !strings.Contains(output, `es_querycost_requests_total{path="/search"} 10`) {
		t.Fatalf("expected 10 search requests in metrics, got:\n%s", output)
	}
	if !strings.Contains(output, `es_querycost_denials_total{reason="forbidden query feature: match_all"}`) {
		t.Fatalf("expected match_all denials in metrics, got:\n%s", output)
	}
}
