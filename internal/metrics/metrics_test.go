package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordRequest(t *testing.T) {
	m := New()
	m.RecordRequest("/search")
	m.RecordRequest("/search")

	if got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("/search")); got != 2 {
		t.Errorf("expected 2 requests, got %v", got)
	}
}

func TestRecordDenial(t *testing.T) {
	m := New()
	m.RecordDenial("cost_limit")

	if got := testutil.ToFloat64(m.DenialsTotal.WithLabelValues("cost_limit")); got != 1 {
		t.Errorf("expected 1 denial, got %v", got)
	}
}

func TestObserveMethods(t *testing.T) {
	m := New()
	m.ObserveEval(0.01)
	m.ObserveProxy(0.02)
	m.ObserveCost(12.5)

	gathered, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, mf := range gathered {
		names[*mf.Name] = true
	}
	for _, name := range []string{
		"es_querycost_evaluation_duration_seconds",
		"es_querycost_proxy_duration_seconds",
		"es_querycost_query_cost",
	} {
		if !names[name] {
			t.Errorf("expected metric %s to be gathered", name)
		}
	}
}

func TestHandler(t *testing.T) {
	m := New()
	m.RecordRequest("/search")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "es_querycost_requests_total") {
		t.Error("expected metrics output to contain request counter")
	}
}
