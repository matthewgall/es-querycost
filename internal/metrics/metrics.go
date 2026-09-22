// Package metrics exposes Prometheus metrics for the query-cost gate.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all collectors used by the gate.
type Metrics struct {
	RequestsTotal *prometheus.CounterVec
	DenialsTotal  *prometheus.CounterVec
	EvalDuration  prometheus.Histogram
	ProxyDuration prometheus.Histogram
	QueryCost     prometheus.Histogram
	Registry      *prometheus.Registry
}

// New creates a Metrics instance with registered collectors.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)

	return &Metrics{
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "es_querycost_requests_total",
			Help: "Total number of requests received by the gate.",
		}, []string{"path"}),

		DenialsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "es_querycost_denials_total",
			Help: "Total number of denied queries by reason.",
		}, []string{"reason"}),

		EvalDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "es_querycost_evaluation_duration_seconds",
			Help:    "Time spent parsing and evaluating query cost.",
			Buckets: prometheus.DefBuckets,
		}),

		ProxyDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "es_querycost_proxy_duration_seconds",
			Help:    "Time spent proxying allowed queries to Elasticsearch.",
			Buckets: prometheus.DefBuckets,
		}),

		QueryCost: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "es_querycost_query_cost",
			Help:    "Observed cost of queries that reached evaluation.",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
		}),

		Registry: reg,
	}
}

// RecordRequest increments the request counter for the given path.
func (m *Metrics) RecordRequest(path string) {
	m.RequestsTotal.WithLabelValues(path).Inc()
}

// RecordDenial increments the denial counter for the given reason.
func (m *Metrics) RecordDenial(reason string) {
	m.DenialsTotal.WithLabelValues(reason).Inc()
}

// ObserveEval records the duration of parse + cost + rule evaluation.
func (m *Metrics) ObserveEval(seconds float64) {
	m.EvalDuration.Observe(seconds)
}

// ObserveProxy records the duration of proxying to Elasticsearch.
func (m *Metrics) ObserveProxy(seconds float64) {
	m.ProxyDuration.Observe(seconds)
}

// ObserveCost records the estimated cost of a query.
func (m *Metrics) ObserveCost(cost float64) {
	m.QueryCost.Observe(cost)
}

// Handler returns an HTTP handler that exposes the metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
