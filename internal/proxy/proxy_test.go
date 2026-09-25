package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"es-querycost/internal/config"
	"es-querycost/internal/metrics"
	"es-querycost/internal/validate"
	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func startMockES(t *testing.T, expectedPath string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != expectedPath {
			t.Errorf("expected path %s, got %s", expectedPath, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	}))
	return s
}

func TestProxyRejectsExpensiveQuery(t *testing.T) {
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = "http://unused.example.com"
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now] AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl* OR filename.keyword:*lidl*)`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("expected 402, got %d", w.Code)
	}
}

func TestProxyForwardsAllowedQuery(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	respBody, _ := io.ReadAll(w.Result().Body)
	if !bytes.Contains(respBody, []byte(`"hits"`)) {
		t.Errorf("expected ES response to be proxied, got %s", string(respBody))
	}
}

func TestProxyInjectsDefaultDateWindow(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyForwardsSourceBody(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
		Source: map[string]any{
			"size": 10,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyRejectsSourceQuery(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
		Source: map[string]any{
			"query": map[string]any{"match_all": map[string]any{}},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for user-supplied source.query, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyRejectsUnknownSourceField(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
		Source: map[string]any{
			"aggs": map[string]any{"by_day": map[string]any{"date_histogram": map[string]any{"field": "@timestamp", "calendar_interval": "1d"}}},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown source field, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyRejectsInvalidMethod(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestProxyRequiresQuery(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	payload := SearchRequest{Index: "my-index"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	cfg := config.Defaults()
	server, _ := NewServer(cfg, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestProxyRejectsInvalidQueryViaES(t *testing.T) {
	cfg := config.Defaults()
	cfg.Validator = "elasticsearch"
	cfg.Elasticsearch.URL = "http://unused.example.com"
	validator := &fakeValidator{valid: false, errMsg: "parse_exception: cannot parse"}
	server, err := NewServerWithValidator(cfg, validator, nil, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{Query: `*:*`, Index: "my-index"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyFallbackToExplanation(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	validator := &fakeValidator{
		valid:       true,
		explanation: "+asn:AS13335",
	}
	server, err := NewServerWithValidator(cfg, validator, nil, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335~`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMetricsEndpoint(t *testing.T) {
	cfg := config.Defaults()
	cfg.MetricsEnabled = true
	cfg.MetricsPath = "/metrics"
	m := metrics.New()
	server, _ := NewServerWithValidator(cfg, validate.NoOp{}, m, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "es_querycost_evaluation_duration_seconds") {
		t.Error("expected metrics output")
	}
}

func TestMetricsRecordedOnDenial(t *testing.T) {
	cfg := config.Defaults()
	m := metrics.New()
	server, _ := NewServerWithValidator(cfg, validate.NoOp{}, m, nil)

	payload := SearchRequest{
		Query: `*:*`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if got := testutil.ToFloat64(m.DenialsTotal.WithLabelValues("forbidden query feature: match_all")); got != 1 {
		t.Errorf("expected 1 denial metric, got %v", got)
	}
}

func TestMetricsRequiresAuthWhenConfigured(t *testing.T) {
	cfg := config.Defaults()
	cfg.MetricsEnabled = true
	cfg.MetricsPath = "/metrics"
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"secret": {UserID: "u1", Plan: "free"},
	}
	m := metrics.New()
	server, _ := NewServerWithValidator(cfg, validate.NoOp{}, m, nil)

	// Without credentials metrics should be unavailable.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated metrics, got %d", w.Code)
	}

	// With valid credentials metrics should be served.
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req2.Header.Set("Authorization", "ApiKey secret")
	w2 := httptest.NewRecorder()
	server.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("expected 200 for authenticated metrics, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestProxyAPIKeyAuth(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"secret": {UserID: "u1", Plan: "free"},
	}
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: "my-index",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	req.Header.Set("Authorization", "ApiKey secret")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyAPIKeyAuthMissing(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"secret": {UserID: "u1", Plan: "free"},
	}
	server, _ := NewServer(cfg, nil)

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: "my-index",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestProxyJWTAuth(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	secret := []byte("jwt-secret")
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	cfg.Auth.Type = "jwt"
	cfg.Auth.JWTSecret = string(secret)
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "user-jwt",
		"plan": "free",
	})
	tokenString, _ := token.SignedString(secret)

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: "my-index",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyAuthContextOverridesRequestPlan(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	cfg.Auth.Type = "apikey"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"secret": {UserID: "u1", Plan: "free"},
	}
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// A leading-wildcard query is denied for "free" but would be allowed for
	// "enterprise". The request body tries to claim "enterprise", but the
	// auth-derived "free" plan must win.
	payload := SearchRequest{
		Query: `*lidl* AND @timestamp:[now-14d TO now]`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "enterprise"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	req.Header.Set("Authorization", "ApiKey secret")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("expected 402 because auth plan 'free' overrides request plan 'enterprise', got %d: %s", w.Code, w.Body.String())
	}
}

type fakeValidator struct {
	valid       bool
	explanation string
	errMsg      string
}

func (f *fakeValidator) Validate(context.Context, string, string) (validate.Result, error) {
	return validate.Result{Valid: f.valid, Explanation: f.explanation, Error: f.errMsg}, nil
}

func TestProxyUpstreamBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	}))
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	cfg.Elasticsearch.Username = "es-user"
	cfg.Elasticsearch.Password = "es-pass"
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-7d TO now]`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotUser != "es-user" || gotPass != "es-pass" {
		t.Errorf("expected ES basic auth es-user:es-pass, got %s:%s", gotUser, gotPass)
	}
}

func TestProxyStripsClientAuthorization(t *testing.T) {
	var gotAuth string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	}))
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-7d TO now]`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("client Authorization header was forwarded to upstream: %s", gotAuth)
	}
}

func TestProxyTimeout(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	cfg.ProxyTimeout = "1ms"
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 when proxy times out, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyCustomPlan(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	cfg.Plans["startup"] = config.Plan{CostLimit: 15, Window: "7d"}
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-7d TO now]`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "startup"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for allowed startup plan query, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyValidateEndpoint(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer server.Close()

	cases := []struct {
		name        string
		query       string
		wantStatus  int
		wantAllowed bool
	}{
		{
			name:        "allowed cheap query",
			query:       `asn:AS13335 AND @timestamp:[now-7d TO now]`,
			wantStatus:  http.StatusOK,
			wantAllowed: true,
		},
		{
			name:        "denied expensive query",
			query:       `*lidl*`,
			wantStatus:  http.StatusPaymentRequired,
			wantAllowed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := SearchRequest{
				Query: tc.query,
				Index: "my-index",
				Context: map[string]any{
					"user": map[string]any{"plan": "free"},
				},
			}
			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", w.Code, tc.wantStatus)
			}
			var resp SearchResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if resp.Allowed != tc.wantAllowed {
				t.Errorf("allowed: got %v, want %v", resp.Allowed, tc.wantAllowed)
			}
			if resp.Cost <= 0 {
				t.Errorf("expected cost > 0, got %f", resp.Cost)
			}
		})
	}
}

func TestProxyValidateMethodNotAllowed(t *testing.T) {
	cfg := config.Defaults()
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/validate", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestServerUpdateConfig(t *testing.T) {
	mock := startMockES(t, "/my-index/_search")
	defer mock.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = mock.URL
	server, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer server.Close()

	newCfg := cfg
	newCfg.Plans["free"] = config.Plan{CostLimit: 0.1, Window: "1d"}
	if err := server.UpdateConfig(newCfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	payload := SearchRequest{
		Query: `asn:AS13335`,
		Index: "my-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("expected denied after lowering free plan limit, got %d", w.Code)
	}
}

func TestParseProxyTimeout(t *testing.T) {
	if d, err := parseProxyTimeout(""); err != nil || d != 30*time.Second {
		t.Errorf("unexpected default: %v, %v", d, err)
	}
	if d, err := parseProxyTimeout("5s"); err != nil || d != 5*time.Second {
		t.Errorf("unexpected duration: %v, %v", d, err)
	}
	if _, err := parseProxyTimeout("not-a-duration"); err == nil {
		t.Error("expected error for invalid duration")
	}
}
