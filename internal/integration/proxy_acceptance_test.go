//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"es-querycost/internal/config"
	"es-querycost/internal/logger"
	"es-querycost/internal/proxy"
)

// proxySearch executes a search through the proxy and returns the decoded ES
// response body. It uses the enterprise plan so the default date window does
// not exclude the freshly seeded documents.
func proxySearch(t *testing.T, server *proxy.Server, query, index string, source map[string]any) map[string]any {
	t.Helper()
	payload := proxy.SearchRequest{
		Query:   query,
		Index:   index,
		Source:  source,
		Context: map[string]any{"user": map[string]any{"plan": "enterprise"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("proxy search returned %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal proxy response: %v", err)
	}
	return res
}

// directSearch executes the same query directly against Elasticsearch and
// returns the decoded response body. The query is wrapped in a query_string
// query to mirror what the proxy sends upstream.
func directSearch(t *testing.T, index, query string, source map[string]any) map[string]any {
	t.Helper()
	bodyMap := map[string]any{
		"query": map[string]any{
			"query_string": map[string]any{"query": query},
		},
	}
	for k, v := range source {
		bodyMap[k] = v
	}
	body, _ := json.Marshal(bodyMap)

	path := "/_search"
	if index != "" {
		path = "/" + index + path
	}
	req, _ := http.NewRequest(http.MethodPost, esURL(t)+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user, pass := esAuth(); user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := esClient().Do(req)
	if err != nil {
		t.Fatalf("direct search request: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct search returned %d: %s", resp.StatusCode, string(data))
	}
	var res map[string]any
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal direct response: %v", err)
	}
	return res
}

func totalHits(m map[string]any) float64 {
	hits, ok := m["hits"].(map[string]any)
	if !ok {
		return -1
	}
	total, ok := hits["total"].(map[string]any)
	if !ok {
		return -1
	}
	v, _ := total["value"].(float64)
	return v
}

func hitCount(m map[string]any) int {
	hits, ok := m["hits"].(map[string]any)
	if !ok {
		return -1
	}
	arr, _ := hits["hits"].([]any)
	return len(arr)
}

func hitASNs(m map[string]any) []string {
	out := []string{}
	hits, ok := m["hits"].(map[string]any)
	if !ok {
		return out
	}
	arr, _ := hits["hits"].([]any)
	for _, h := range arr {
		doc, _ := h.(map[string]any)
		src, _ := doc["_source"].(map[string]any)
		asn, _ := src["asn"].(string)
		if asn != "" {
			out = append(out, asn)
		}
	}
	sort.Strings(out)
	return out
}

func TestProxyMockResponseBodyPassthrough(t *testing.T) {
	upstreamBody := `{"took":7,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},"hits":{"total":{"value":3,"relation":"eq"},"max_score":1.0,"hits":[{"_id":"a"},{"_id":"b"},{"_id":"c"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Es-Custom", "preserved")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query:   `asn:AS13335`,
		Index:   "my-index",
		Context: map[string]any{"user": map[string]any{"plan": "free"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != upstreamBody {
		t.Errorf("proxy changed response body\nwant: %s\n got: %s", upstreamBody, got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %q", ct)
	}
	if got := w.Header().Get("X-Es-Custom"); got != "preserved" {
		t.Errorf("expected upstream custom header preserved, got %q", got)
	}
}

func TestProxyMockQueryParamForwarded(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query:   `asn:AS13335`,
		Index:   "my-index",
		Context: map[string]any{"user": map[string]any{"plan": "free"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	want := "+(asn:AS13335) +@timestamp:[now-14d TO now]"
	if gotQuery != want {
		t.Errorf("upstream query param mismatch\nwant: %q\n got: %q", want, gotQuery)
	}
}

func TestProxyMockSourceBodyForwarded(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	source := map[string]any{
		"size":             float64(5),
		"from":             float64(10),
		"sort":             []any{map[string]any{"asn": "desc"}},
		"track_total_hits": true,
	}

	payload := proxy.SearchRequest{
		Query:   `asn:AS13335`,
		Index:   "my-index",
		Source:  source,
		Context: map[string]any{"user": map[string]any{"plan": "free"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if got := gotBody["size"]; got != float64(5) {
		t.Errorf("source.size not forwarded: got %v", got)
	}
	if got := gotBody["from"]; got != float64(10) {
		t.Errorf("source.from not forwarded: got %v", got)
	}
	if got := gotBody["track_total_hits"]; got != true {
		t.Errorf("source.track_total_hits not forwarded: got %v", got)
	}
	if got, ok := gotBody["sort"].([]any); !ok || len(got) != 1 {
		t.Errorf("source.sort not forwarded: got %v", gotBody["sort"])
	}

	q, ok := gotBody["query"].(map[string]any)
	if !ok {
		t.Fatalf("injected query missing from upstream body: %v", gotBody)
	}
	qs, ok := q["query_string"].(map[string]any)
	if !ok {
		t.Fatalf("injected query_string missing: %v", gotBody)
	}
	want := "+(asn:AS13335) +@timestamp:[now-14d TO now]"
	if got := qs["query"]; got != want {
		t.Errorf("injected query string mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestProxyMockUpstreamsStatusAndHeaders(t *testing.T) {
	for _, wantStatus := range []int{http.StatusOK, http.StatusNotFound, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", wantStatus), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Es-Custom", fmt.Sprintf("status-%d", wantStatus))
				w.WriteHeader(wantStatus)
				if wantStatus != http.StatusOK {
					_, _ = w.Write([]byte(`{"error":{"reason":"upstream error"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
			}))
			defer srv.Close()

			cfg := config.Defaults()
			cfg.Elasticsearch.URL = srv.URL
			server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
			if err != nil {
				t.Fatalf("create server: %v", err)
			}

			payload := proxy.SearchRequest{
				Query:   `asn:AS13335`,
				Index:   "my-index",
				Context: map[string]any{"user": map[string]any{"plan": "free"}},
			}
			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, req)

			if w.Code != wantStatus {
				t.Fatalf("expected status %d, got %d: %s", wantStatus, w.Code, w.Body.String())
			}
			ct := w.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("expected JSON content type from upstream, got %q", ct)
			}
			if got := w.Header().Get("X-Es-Custom"); got != fmt.Sprintf("status-%d", wantStatus) {
				t.Errorf("upstream custom header not preserved, got %q", got)
			}
		})
	}
}

func TestProxyLiveRoundTripSimpleQueries(t *testing.T) {
	skipIfESUnreachable(t)
	index := "es-querycost-test-accept"
	createIndex(t, index)
	seedAcceptanceDocs(t, index)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"term", `asn:AS11111`, []string{"AS11111"}},
		{"boolean_or", `asn:AS11111 OR asn:AS22222`, []string{"AS11111", "AS22222"}},
		{"boolean_and", `asn:AS11111 AND page.url.keyword:"https://accept.example.com/one"`, []string{"AS11111"}},
		{"wildcard", `asn:AS*`, []string{"AS11111", "AS22222", "AS33333"}},
		{"range", `asn:AS11111 AND @timestamp:[now-30d TO now]`, []string{"AS11111"}},
		{"phrase_on_url", `page.url:"/one"`, []string{"AS11111"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxyRes := proxySearch(t, server, tc.query, index, nil)
			directRes := directSearch(t, index, tc.query, nil)

			if totalHits(proxyRes) != totalHits(directRes) {
				t.Errorf("hits.total mismatch: proxy=%v direct=%v", totalHits(proxyRes), totalHits(directRes))
			}
			if got := hitASNs(proxyRes); !slicesEqual(got, tc.want) {
				t.Errorf("proxy asns = %v, want %v", got, tc.want)
			}
			if !slicesEqual(hitASNs(proxyRes), hitASNs(directRes)) {
				t.Errorf("returned documents differ\nproxy: %v\ndirect: %v", hitASNs(proxyRes), hitASNs(directRes))
			}
		})
	}
}

func TestProxyLiveSourceFieldsRoundTrip(t *testing.T) {
	skipIfESUnreachable(t)
	index := "es-querycost-test-accept"
	createIndex(t, index)
	seedAcceptanceDocs(t, index)

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	source := map[string]any{
		"size":   float64(1),
		"from":   float64(1),
		"sort":   []any{map[string]any{"asn": "asc"}},
		"_source": []string{"asn"},
	}

	proxyRes := proxySearch(t, server, `asn:AS*`, index, source)
	directRes := directSearch(t, index, `asn:AS*`, source)

	if got, want := hitCount(proxyRes), hitCount(directRes); got != want {
		t.Errorf("hit count mismatch: proxy=%d direct=%d", got, want)
	}
	if totalHits(proxyRes) != totalHits(directRes) {
		t.Errorf("total hits mismatch: proxy=%v direct=%v", totalHits(proxyRes), totalHits(directRes))
	}

	for _, h := range hitSources(proxyRes) {
		if _, ok := h["page"]; ok {
			t.Errorf("proxy returned field excluded by _source: %v", h)
		}
	}
}

func TestProxyLiveDefaultWindowFiltersButDoesNotMangle(t *testing.T) {
	skipIfESUnreachable(t)
	index := "es-querycost-test-accept"
	createIndex(t, index)

	oldTS := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	indexDocumentWithID(t, index, "1", fmt.Sprintf(`{"asn":"AS_OLD","@timestamp":%q,"page":{"url":"https://accept.example.com/old"}}`, oldTS))
	recentTS := time.Now().UTC().Format(time.RFC3339)
	indexDocumentWithID(t, index, "2", fmt.Sprintf(`{"asn":"AS_RECENT","@timestamp":%q,"page":{"url":"https://accept.example.com/recent"}}`, recentTS))

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	// Direct search with no date window returns both documents.
	directRes := directSearch(t, index, `asn:AS*`, nil)
	if got := totalHits(directRes); got != 2 {
		t.Fatalf("direct search should return 2 docs, got %v", got)
	}

	// Free plan proxy injects a 14-day window, so only the recent document is returned.
	payload := proxy.SearchRequest{
		Query:   `asn:AS*`,
		Index:   index,
		Context: map[string]any{"user": map[string]any{"plan": "free"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("proxy search returned %d: %s", w.Code, w.Body.String())
	}
	var proxyRes map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &proxyRes); err != nil {
		t.Fatalf("unmarshal proxy response: %v", err)
	}
	if got := totalHits(proxyRes); got != 1 {
		t.Errorf("expected exactly one recent hit with free window, got %v", got)
	}
	if got := hitASNs(proxyRes); !slicesEqual(got, []string{"AS_RECENT"}) {
		t.Errorf("expected only AS_RECENT, got %v", got)
	}
	// But the single hit must be byte-identical to the direct result for that doc.
	directAsns := hitASNs(directRes)
	if !contains(directAsns, "AS_RECENT") {
		t.Fatalf("direct result does not contain AS_RECENT: %v", directAsns)
	}
}

func TestProxyLiveIndexNotFoundPassthrough(t *testing.T) {
	skipIfESUnreachable(t)
	index := "es-querycost-test-absent"

	cfg := esConfig(t)
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
		Query:   `asn:AS13335`,
		Index:   index,
		Context: map[string]any{"user": map[string]any{"plan": "enterprise"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing index, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`index_not_found_exception`)) && !bytes.Contains(w.Body.Bytes(), []byte(`no such index`)) {
		t.Errorf("expected upstream error JSON in body, got: %s", w.Body.String())
	}
}

func seedAcceptanceDocs(t *testing.T, index string) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339)
	indexDocumentWithID(t, index, "1", fmt.Sprintf(`{"asn":"AS11111","@timestamp":%q,"page":{"url":"https://accept.example.com/one"}}`, ts))
	indexDocumentWithID(t, index, "2", fmt.Sprintf(`{"asn":"AS22222","@timestamp":%q,"page":{"url":"https://accept.example.com/two"}}`, ts))
	indexDocumentWithID(t, index, "3", fmt.Sprintf(`{"asn":"AS33333","@timestamp":%q,"page":{"url":"https://accept.example.com/three"}}`, ts))
}

func indexDocumentWithID(t *testing.T, index, id, doc string) {
	t.Helper()
	url := fmt.Sprintf("%s/%s/_doc/%s?refresh=wait_for", esURL(t), index, id)
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
		t.Fatalf("index document %s/%s failed: %d %s", index, id, resp.StatusCode, string(body))
	}
}

func hitSources(m map[string]any) []map[string]any {
	out := []map[string]any{}
	hits, ok := m["hits"].(map[string]any)
	if !ok {
		return out
	}
	arr, _ := hits["hits"].([]any)
	for _, h := range arr {
		doc, _ := h.(map[string]any)
		if src, ok := doc["_source"].(map[string]any); ok {
			out = append(out, src)
		}
	}
	return out
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
