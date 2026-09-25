//go:build integration

package integration

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"es-querycost/internal/config"
	"es-querycost/internal/logger"
	"es-querycost/internal/proxy"
)

func esSearchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0},"hits":[]}}`))
	}
}

func TestProxyWithBasicAuthSuccess(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		esSearchHandler()(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Elasticsearch.Username = "elastic"
	cfg.Elasticsearch.Password = "changeme"
	callAndExpect(t, cfg, http.StatusOK)

	if gotUser != "elastic" || gotPass != "changeme" {
		t.Errorf("expected basic auth elastic:changeme, got %s:%s", gotUser, gotPass)
	}
}

func TestProxyWithBasicAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "elastic" || pass != "changeme" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
			return
		}
		esSearchHandler()(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Elasticsearch.Username = "elastic"
	cfg.Elasticsearch.Password = "wrong"
	callAndExpect(t, cfg, http.StatusUnauthorized)
}

func TestProxyWithTLSSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Elasticsearch.InsecureSkipVerify = true
	callAndExpect(t, cfg, http.StatusOK)
}

func TestProxyWithTLSAndCustomCA(t *testing.T) {
	certs := generateTestCerts(t)
	caFile := writeTempFile(t, certPEM(certs.caCert.Raw))

	srv := httptest.NewUnstartedServer(esSearchHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{certs.serverCert}}
	srv.StartTLS()
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Elasticsearch.CACert = caFile
	callAndExpect(t, cfg, http.StatusOK)
}

func TestProxyWithTLSNoVerifyFails(t *testing.T) {
	srv := httptest.NewTLSServer(esSearchHandler())
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	// InsecureSkipVerify is false and no CA is configured.
	callAndExpect(t, cfg, http.StatusBadGateway)
}

func TestProxyWithInvalidElasticsearchURL(t *testing.T) {
	cfg := config.Defaults()
	cfg.Elasticsearch.URL = "http://localhost:1" // port 1 should refuse connection
	callAndExpect(t, cfg, http.StatusBadGateway)
}

func TestProxyWithMTLSUnsupported(t *testing.T) {
	certs := generateTestCerts(t)

	srv := httptest.NewUnstartedServer(esSearchHandler())
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM(certs.caCert.Raw))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{certs.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	srv.StartTLS()
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Elasticsearch.URL = srv.URL
	cfg.Elasticsearch.InsecureSkipVerify = true
	// No client certificate is configured, so the handshake should fail.
	callAndExpect(t, cfg, http.StatusBadGateway)
}

func callAndExpect(t *testing.T, cfg config.Config, wantStatus int) {
	t.Helper()
	server, err := proxy.NewServer(cfg, logger.New(logger.Defaults(), nil))
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	payload := proxy.SearchRequest{
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

	if w.Code != wantStatus {
		t.Fatalf("expected status %d, got %d: %s", wantStatus, w.Code, w.Body.String())
	}
}
