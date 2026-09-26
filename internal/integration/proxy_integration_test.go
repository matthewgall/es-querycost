//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"es-querycost/internal/config"
	"es-querycost/internal/logger"
	"es-querycost/internal/proxy"
)

func esURL(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("ESQUERY_TEST_ELASTICSEARCH_URL"); v != "" {
		return v
	}
	return "http://localhost:9200"
}

func esAuth() (string, string) {
	return os.Getenv("ESQUERY_TEST_ELASTICSEARCH_USERNAME"), os.Getenv("ESQUERY_TEST_ELASTICSEARCH_PASSWORD")
}

func esConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = esURL(t)
	if user, pass := esAuth(); user != "" {
		cfg.Elasticsearch.Username = user
		cfg.Elasticsearch.Password = pass
	}
	if os.Getenv("ESQUERY_TEST_ELASTICSEARCH_INSECURE_SKIP_VERIFY") == "true" {
		cfg.Elasticsearch.InsecureSkipVerify = true
	}
	return cfg
}

func esInsecureSkipVerify() bool {
	return os.Getenv("ESQUERY_TEST_ELASTICSEARCH_INSECURE_SKIP_VERIFY") == "true"
}

func esClient() *http.Client {
	if esInsecureSkipVerify() {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		return &http.Client{Transport: tr}
	}
	return http.DefaultClient
}

func skipIfESUnreachable(t *testing.T) {
	t.Helper()
	if os.Getenv("ESQUERY_TEST_ELASTICSEARCH_URL") == "" && os.Getenv("CI") != "" {
		t.Skip("skipping integration test; set ESQUERY_TEST_ELASTICSEARCH_URL to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, esURL(t), nil)
	if user, pass := esAuth(); user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := esClient().Do(req)
	if err != nil {
		t.Skipf("elasticsearch unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("elasticsearch returned %d", resp.StatusCode)
	}
}

func ensureIndex(t *testing.T) string {
	t.Helper()
	index := "es-querycost-test"
	url := esURL(t) + "/" + index
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(`{
		"settings": {"number_of_shards": 1, "number_of_replicas": 0},
		"mappings": {
			"properties": {
				"asn": {"type": "keyword"},
				"page": {"properties": {"url": {"type": "text", "fields": {"keyword": {"type": "keyword"}}}}},
				"@timestamp": {"type": "date"}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	if user, pass := esAuth(); user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := esClient().Do(req)
	if err != nil {
		t.Fatalf("create index: %v", err)
	}
	defer resp.Body.Close()
	// 400 likely means the index already exists.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create index failed: %d %s", resp.StatusCode, string(body))
	}
	return index
}

func seedDocument(t *testing.T, index string) {
	t.Helper()
	doc := fmt.Sprintf(`{"asn":"AS13335","@timestamp":%q,"page":{"url":"https://example.com/foo"}}`, time.Now().UTC().Format(time.RFC3339))
	indexDocument(t, index, doc)
}

func indexDocument(t *testing.T, index, doc string) {
	t.Helper()
	url := fmt.Sprintf("%s/%s/_doc/1?refresh=wait_for", esURL(t), index)
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(doc))
	req.Header.Set("Content-Type", "application/json")
	if user, pass := esAuth(); user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := esClient().Do(req)
	if err != nil {
		t.Fatalf("index document: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("index document failed: %d %s", resp.StatusCode, string(body))
	}
}

func TestProxyAgainstElasticsearch(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	seedDocument(t, index)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: index,
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"AS13335"`)) {
		t.Errorf("expected search response to contain seeded document, got %s", w.Body.String())
	}
}

func TestProxyAgainstElasticsearchInvertedQuery(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	seedDocument(t, index)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query: `asn:AS64496 AND @timestamp:[now-14d TO now]`,
		Index: index,
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"AS13335"`)) {
		t.Errorf("inverted query unexpectedly returned seeded document: %s", w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"value":0`)) {
		t.Errorf("expected zero hits, got: %s", w.Body.String())
	}
}

func TestProxyDeniesExpensiveQueryAgainstElasticsearch(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query: `asn:AS13335 AND (page.url.keyword:*lidl* OR page.url.keyword:*lidl*)`,
		Index: index,
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("expected 402, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyDefaultWindowExcludesOldDocument(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	oldTimestamp := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	oldDoc := fmt.Sprintf(`{"asn":"AS-OLD","@timestamp":%q,"page":{"url":"https://example.com/old"}}`, oldTimestamp)
	indexDocument(t, index, oldDoc)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// No explicit date window in the query; the gate should inject the free
	// plan window (14d), which excludes the 40-day-old document.
	payload := proxy.SearchRequest{
		Query: `asn:AS-OLD`,
		Index: index,
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"AS-OLD"`)) {
		t.Errorf("old document unexpectedly matched default window: %s", w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"value":0`)) {
		t.Errorf("expected zero hits with default window, got: %s", w.Body.String())
	}
}

func TestProxyMetricsEndpoint(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	seedDocument(t, index)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// Make a request so the metrics counter is incremented.
	payload := proxy.SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: index,
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	searchReq := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	server.Handler().ServeHTTP(httptest.NewRecorder(), searchReq)

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, metricsReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`es_querycost_requests_total{path="/search"} 1`)) {
		t.Errorf("expected /metrics to show a /search request, got: %s", w.Body.String())
	}
}

func TestProxyValidateEndpoint(t *testing.T) {
	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query: `asn:AS13335`,
		Index: "any-index",
		Context: map[string]any{
			"user": map[string]any{"plan": "free"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /validate, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Errorf("expected validate response to allow cheap query, got: %s", w.Body.String())
	}
}

func TestProxyEnterprisePlanAllowsExpensiveQuery(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query: `asn:AS13335 AND (page.url.keyword:*lidl* OR page.url.keyword:*lidl*)`,
		Index: index,
		Context: map[string]any{
			"user": map[string]any{"plan": "enterprise"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected enterprise plan to allow expensive query, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProxyAPIKeyAuth(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)
	seedDocument(t, index)

	cfg := esConfig(t)
	cfg.Auth.Type = "api_key"
	cfg.Auth.APIKey = map[string]config.ContextFromConfig{
		"live-test-key": {Plan: "free"},
	}
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query: `asn:AS13335 AND @timestamp:[now-14d TO now]`,
		Index: index,
	}
	body, _ := json.Marshal(payload)

	reqMissing := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	wMissing := httptest.NewRecorder()
	server.Handler().ServeHTTP(wMissing, reqMissing)
	if wMissing.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without API key, got %d: %s", wMissing.Code, wMissing.Body.String())
	}

	reqValid := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	reqValid.Header.Set("Authorization", "Apikey live-test-key")
	wValid := httptest.NewRecorder()
	server.Handler().ServeHTTP(wValid, reqValid)
	if wValid.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid API key, got %d: %s", wValid.Code, wValid.Body.String())
	}
	if !bytes.Contains(wValid.Body.Bytes(), []byte(`"AS13335"`)) {
		t.Errorf("expected valid API key request to return seeded document, got: %s", wValid.Body.String())
	}
}
