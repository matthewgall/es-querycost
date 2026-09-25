//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
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
	resp, err := http.DefaultClient.Do(req)
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
	resp, err := http.DefaultClient.Do(req)
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

func TestProxyAgainstElasticsearch(t *testing.T) {
	skipIfESUnreachable(t)
	index := ensureIndex(t)

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
	if !bytes.Contains(w.Body.Bytes(), []byte(`"hits"`)) {
		t.Errorf("expected ES response, got %s", w.Body.String())
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
