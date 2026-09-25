// Package proxy provides the HTTP query-cost gate and Elasticsearch forwarding.
package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"es-querycost/internal/auth"
	"es-querycost/internal/config"
	"es-querycost/internal/cost"
	"es-querycost/internal/logger"
	"es-querycost/internal/metrics"
	"es-querycost/internal/query"
	"es-querycost/internal/rules"
	"es-querycost/internal/validate"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB

// SearchRequest is the body expected by the gate's /search endpoint.
type SearchRequest struct {
	Query   string         `json:"query"`
	Index   string         `json:"index"`
	Context map[string]any `json:"context"`
	// Source is an optional raw Elasticsearch query body. If empty the query
	// is forwarded as a URI query parameter.
	Source map[string]any `json:"source,omitempty"`
}

// SearchResponse is returned when a query is denied.
type SearchResponse struct {
	Allowed bool        `json:"allowed"`
	Cost    float64     `json:"cost"`
	Reason  string      `json:"reason,omitempty"`
	Report  cost.Report `json:"report,omitempty"`
}

// Server wires together the cost gate and the Elasticsearch proxy.
type Server struct {
	cfg           config.Config
	model         cost.Model
	engine        *rules.Engine
	validator     validate.Validator
	authenticator auth.Authenticator
	metrics       *metrics.Metrics
	logger        *slog.Logger
	client        *http.Client
	target        *url.URL
}

// NewServer creates a Server. It returns an error if the Elasticsearch URL is invalid.
func NewServer(cfg config.Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	validator, err := cfg.BuildValidator()
	if err != nil {
		return nil, fmt.Errorf("build validator: %w", err)
	}
	var m *metrics.Metrics
	if cfg.MetricsEnabled {
		m = metrics.New()
	}
	return newServer(cfg, validator, cfg.BuildAuthenticator(), m, logger)
}

// NewServerWithValidator is mainly for tests.
func NewServerWithValidator(cfg config.Config, validator validate.Validator, m *metrics.Metrics, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return newServer(cfg, validator, cfg.BuildAuthenticator(), m, logger)
}

func newServer(cfg config.Config, validator validate.Validator, authenticator auth.Authenticator, m *metrics.Metrics, logger *slog.Logger) (*Server, error) {
	target, err := url.Parse(cfg.Elasticsearch.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid elasticsearch url: %w", err)
	}
	timeout, err := parseProxyTimeout(cfg.ProxyTimeout)
	if err != nil {
		return nil, err
	}
	client, err := newProxyClient(cfg, timeout)
	if err != nil {
		return nil, fmt.Errorf("proxy http client: %w", err)
	}
	return &Server{
		cfg:           cfg,
		model:         cfg.BuildCostModel(),
		engine:        cfg.BuildEngine(),
		validator:     validator,
		authenticator: authenticator,
		metrics:       m,
		logger:        logger,
		client:        client,
		target:        target,
	}, nil
}

func newProxyClient(cfg config.Config, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{}
	if cfg.Elasticsearch.InsecureSkipVerify || cfg.Elasticsearch.CACert != "" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.Elasticsearch.InsecureSkipVerify,
		}
		if cfg.Elasticsearch.CACert != "" {
			caCert, err := os.ReadFile(cfg.Elasticsearch.CACert)
			if err != nil {
				return nil, fmt.Errorf("read elasticsearch_ca_cert: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse elasticsearch_ca_cert")
			}
			tlsConfig.RootCAs = pool
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// Handler returns the http.Handler for the gate.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/validate", s.handleValidate)
	mux.HandleFunc("/healthz", s.handleHealthz)
	if s.metrics != nil && s.cfg.MetricsPath != "" {
		if _, ok := s.authenticator.(auth.NoOp); ok {
			mux.Handle(s.cfg.MetricsPath, s.metrics.Handler())
		} else {
			mux.Handle(s.cfg.MetricsPath, s.requireAuth(s.metrics.Handler()))
		}
	}
	handler := logger.Middleware(s.logger, s.cfg.Logging.Requests)(mux)
	return handler
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.metrics != nil {
		s.metrics.RecordRequest("/search")
	}

	req, ok := s.parseAndAuth(w, r, "/search")
	if !ok {
		return
	}

	evalStart := time.Now()
	decision, ok := s.evaluate(w, r, req, evalStart)
	if !ok {
		return
	}
	if !decision.Allowed {
		respondJSON(w, http.StatusPaymentRequired, SearchResponse{
			Allowed: false,
			Cost:    decision.Report.Cost,
			Reason:  decision.Reason,
			Report:  decision.Report,
		})
		return
	}

	query := s.injectDefaultWindow(decision.AST, req.Context)

	upstream, err := s.buildUpstreamURL(req, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	outBody, err := s.buildRequestBody(req, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, outBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	copyHeaders(outReq.Header, r.Header)
	if s.cfg.Elasticsearch.Username != "" {
		outReq.SetBasicAuth(s.cfg.Elasticsearch.Username, s.cfg.Elasticsearch.Password)
	}
	if outBody != nil {
		outReq.Header.Set("Content-Type", "application/json")
	}

	proxyStart := time.Now()
	resp, err := s.client.Do(outReq)
	if s.metrics != nil {
		s.metrics.ObserveProxy(time.Since(proxyStart).Seconds())
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		if s.logger != nil {
			s.logger.Error("error copying response", slog.String("error", err.Error()))
		}
	}
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.metrics != nil {
		s.metrics.RecordRequest("/validate")
	}

	req, ok := s.parseAndAuth(w, r, "/validate")
	if !ok {
		return
	}

	evalStart := time.Now()
	decision, ok := s.evaluate(w, r, req, evalStart)
	if !ok {
		return
	}

	status := http.StatusOK
	if !decision.Allowed {
		status = http.StatusPaymentRequired
	}
	respondJSON(w, status, SearchResponse{
		Allowed: decision.Allowed,
		Cost:    decision.Report.Cost,
		Reason:  decision.Reason,
		Report:  decision.Report,
	})
}

func (s *Server) parseAndAuth(w http.ResponseWriter, r *http.Request, path string) (SearchRequest, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return SearchRequest{}, false
	}
	defer r.Body.Close()

	var req SearchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return SearchRequest{}, false
	}
	if req.Query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return SearchRequest{}, false
	}

	authCtx, err := s.authenticator.Authenticate(r)
	if err != nil {
		if s.metrics != nil {
			s.metrics.RecordDenial("auth_failed")
		}
		if s.logger != nil {
			s.logger.Warn("authentication failed", traceAttr(r.Context()), slog.String("error", err.Error()))
		}
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return SearchRequest{}, false
	}
	// Merge auth-derived identity. When authentication is configured, auth values
	// must override anything supplied in the request body to prevent users from
	// escalating their plan via context.user.plan. In no-auth mode the request
	// body is the only source of identity, so it is retained.
	if _, ok := s.authenticator.(auth.NoOp); ok {
		req.Context = mergeContext(authCtx.ToMap(), req.Context)
	} else {
		req.Context = mergeContext(req.Context, authCtx.ToMap())
	}
	return req, true
}

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request, req SearchRequest, evalStart time.Time) (evalDecision, bool) {
	ast, queryUsed, err := s.resolveQuery(r.Context(), req.Query, req.Index)
	if err != nil {
		if s.metrics != nil {
			s.metrics.RecordDenial("invalid_query")
			s.metrics.ObserveEval(time.Since(evalStart).Seconds())
		}
		if s.logger != nil {
			s.logger.Warn("query denied", traceAttr(r.Context()), slog.String("reason", err.Error()), slog.String("phase", "validation"))
		}
		respondJSON(w, http.StatusBadRequest, SearchResponse{
			Allowed: false,
			Reason:  err.Error(),
		})
		return evalDecision{}, false
	}

	report := cost.Estimate(ast, s.model)
	decision := s.engine.Evaluate(queryUsed, ast, report, req.Context)
	if s.metrics != nil {
		s.metrics.ObserveEval(time.Since(evalStart).Seconds())
		s.metrics.ObserveCost(report.Cost)
	}
	if !decision.Allowed {
		if s.metrics != nil {
			s.metrics.RecordDenial(decision.Reason)
		}
		if s.logger != nil {
			s.logger.Warn("query denied", traceAttr(r.Context()), slog.String("reason", decision.Reason), slog.Float64("cost", decision.Report.Cost))
		}
	}
	return evalDecision{Allowed: decision.Allowed, Report: decision.Report, Reason: decision.Reason, AST: ast}, true
}

type evalDecision struct {
	Allowed bool
	Reason  string
	Report  cost.Report
	AST     query.Node
}

func (s *Server) resolveQuery(ctx context.Context, queryStr, index string) (query.Node, string, error) {
	ast, err := query.Parse(queryStr)
	if err == nil && s.cfg.Validator != "elasticsearch" {
		return ast, queryStr, nil
	}

	result, vErr := s.validator.Validate(ctx, index, queryStr)
	if vErr != nil {
		return nil, "", fmt.Errorf("elasticsearch validation failed: %w", vErr)
	}
	if !result.Valid {
		return nil, "", fmt.Errorf("invalid query: %s", result.Error)
	}

	if err == nil {
		return ast, queryStr, nil
	}

	// Local parse failed but Elasticsearch accepted the query. Try to parse
	// its normalised explanation so we can still estimate cost.
	if result.Explanation != "" {
		if expAST, expErr := query.Parse(result.Explanation); expErr == nil {
			return expAST, result.Explanation, nil
		}
	}

	return nil, "", fmt.Errorf("query syntax not supported for cost estimation: %v", err)
}

func (s *Server) injectDefaultWindow(ast query.Node, ctx map[string]any) string {
	if s.cfg.RequireWindow {
		return astToString(ast)
	}
	qr := rules.QueryWindow{DateFields: s.cfg.DateFields}
	if _, found, _ := qr.FindWindow(ast, time.Now()); found {
		return astToString(ast)
	}
	plan := rules.PlanFromContext(ctx)
	window := s.cfg.PlanWindow(plan)
	field := s.cfg.DateField
	if field == "" {
		field = "@timestamp"
	}

	// Build the final query from the AST so user-supplied punctuation cannot
	// break out of the injected date-window clause.
	injected := query.Boolean{
		Clauses: []query.Clause{
			{Occur: query.Must, Term: query.Group{Query: ast}},
			{
				Occur: query.Must,
				Field: field,
				Term: query.Range{
					Low:           fmt.Sprintf("now-%s", window),
					High:          "now",
					InclusiveLow:  true,
					InclusiveHigh: true,
				},
			},
		},
	}
	return injected.String()
}

// astToString safely serialises a query AST to a Lucene query string.
func astToString(n query.Node) string {
	if n == nil {
		return ""
	}
	return n.String()
}

func (s *Server) buildRequestBody(req SearchRequest, query string) (io.Reader, error) {
	if req.Source == nil {
		return nil, nil
	}
	// Copy so we do not mutate the parsed request body.
	src := make(map[string]any, len(req.Source)+1)
	for k, v := range req.Source {
		if k == "query" {
			return nil, fmt.Errorf("source.query is not allowed; use the top-level query field")
		}
		if !allowedSourceFields[k] {
			return nil, fmt.Errorf("source field %q is not allowed", k)
		}
		src[k] = v
	}
	src["query"] = map[string]any{"query_string": map[string]any{"query": query}}
	b, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("marshal source body: %w", err)
	}
	return bytes.NewReader(b), nil
}

var allowedSourceFields = map[string]bool{
	"size":             true,
	"from":             true,
	"sort":             true,
	"_source":          true,
	"fields":           true,
	"track_total_hits": true,
	"collapse":         true,
}

func (s *Server) buildUpstreamURL(req SearchRequest, query string) (string, error) {
	index := strings.Trim(req.Index, "/")
	path := "/_search"
	if index != "" {
		path = "/" + index + path
	}
	upstream := strings.TrimRight(s.target.String(), "/") + path
	if req.Source == nil {
		u, err := url.Parse(upstream)
		if err != nil {
			return "", err
		}
		q := u.Query()
		q.Set("q", query)
		u.RawQuery = q.Encode()
		upstream = u.String()
	}
	return upstream, nil
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if lk == "content-length" || lk == "host" || lk == "authorization" || lk == "cookie" || lk == "set-cookie" || lk == "proxy-authorization" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.authenticator.Authenticate(r); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// mergeContext returns a new map with base values overridden by values from
// override. It avoids mutating either input.
func mergeContext(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func traceAttr(ctx context.Context) slog.Attr {
	return slog.String("trace_id", logger.TraceIDFromContext(ctx))
}

func parseProxyTimeout(s string) (time.Duration, error) {
	if s == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid proxy_timeout %q: %w", s, err)
	}
	return d, nil
}

func (s *Server) UpdateConfig(cfg config.Config) error {
	timeout, err := parseProxyTimeout(cfg.ProxyTimeout)
	if err != nil {
		return err
	}
	target, err := url.Parse(cfg.Elasticsearch.URL)
	if err != nil {
		return fmt.Errorf("invalid elasticsearch url: %w", err)
	}

	oldAuth := s.authenticator
	client, err := newProxyClient(cfg, timeout)
	if err != nil {
		return err
	}
	s.cfg = cfg
	s.model = cfg.BuildCostModel()
	s.engine = cfg.BuildEngine()
	s.validator, err = cfg.BuildValidator()
	if err != nil {
		return fmt.Errorf("build validator: %w", err)
	}
	s.authenticator = cfg.BuildAuthenticator()
	s.client = client
	s.target = target

	go func() {
		if err := oldAuth.Close(); err != nil {
			if s.logger != nil {
				s.logger.Warn("failed to close previous authenticator", slog.String("error", err.Error()))
			}
		}
	}()
	return nil
}

// Close releases resources held by the server.
func (s *Server) Close() error {
	if s.authenticator != nil {
		return s.authenticator.Close()
	}
	return nil
}
