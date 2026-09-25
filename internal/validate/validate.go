// Package validate provides query validation against Elasticsearch.
package validate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxValidateResponseBytes = 1 << 20 // 1 MiB

// Result is the outcome of validating a query.
type Result struct {
	Valid       bool
	Explanation string
	Error       string
}

// Validator checks whether a Lucene query is syntactically valid.
type Validator interface {
	Validate(ctx context.Context, index, query string) (Result, error)
}

// NoOp always reports the query as valid. It is used when validation is disabled.
type NoOp struct{}

func (NoOp) Validate(context.Context, string, string) (Result, error) {
	return Result{Valid: true}, nil
}

// ES uses Elasticsearch's _validate/query API to validate a query.
type ES struct {
	BaseURL string
	Client  *http.Client
}

// NewES creates an Elasticsearch-backed validator.
func NewES(baseURL string, insecureSkipVerify bool, caCert string) (*ES, error) {
	transport := &http.Transport{}
	if insecureSkipVerify || caCert != "" {
		tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify}
		if caCert != "" {
			cert, err := os.ReadFile(caCert)
			if err != nil {
				return nil, fmt.Errorf("read ca cert: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(cert) {
				return nil, fmt.Errorf("parse ca cert")
			}
			tlsConfig.RootCAs = pool
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &ES{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: 10 * time.Second, Transport: transport},
	}, nil
}

// Validate sends the query to Elasticsearch and returns whether it is valid.
func (v *ES) Validate(ctx context.Context, index, query string) (Result, error) {
	path := "/_validate/query"
	if index != "" {
		path = "/" + strings.Trim(index, "/") + path
	}
	path += "?explain=true"

	body, err := json.Marshal(map[string]any{
		"query": map[string]any{
			"query_string": map[string]any{"query": query},
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.Parse(v.BaseURL + path)
	if err != nil {
		return Result{}, fmt.Errorf("parse url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.Client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("validate request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxValidateResponseBytes))
	if err != nil {
		return Result{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Result{Valid: false, Error: string(respBody)}, nil
	}

	var parsed esValidateResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Result{Valid: false, Error: string(respBody)}, nil
	}

	// Gather all explanations. For a single index this will be one explanation.
	var explanations []string
	for _, shard := range parsed.Shards.Failures {
		if shard.Reason != nil {
			return Result{Valid: false, Error: shard.String()}, nil
		}
	}
	for _, exp := range parsed.Explanations {
		if !exp.Valid {
			return Result{Valid: false, Error: exp.Explanation}, nil
		}
		explanations = append(explanations, exp.Explanation)
	}

	if !parsed.Valid {
		return Result{Valid: false, Error: string(respBody)}, nil
	}

	return Result{Valid: true, Explanation: strings.Join(explanations, " ")}, nil
}

type esValidateResponse struct {
	Valid        bool            `json:"valid"`
	Explanations []esExplanation `json:"explanations"`
	Shards       esShards        `json:"_shards"`
}

type esExplanation struct {
	Index       string `json:"index"`
	Valid       bool   `json:"valid"`
	Explanation string `json:"explanation"`
}

type esShards struct {
	Failures []esShardFailure `json:"failures"`
}

type esShardFailure struct {
	Reason *json.RawMessage `json:"reason"`
}

func (f *esShardFailure) String() string {
	if f.Reason == nil {
		return "unknown shard failure"
	}
	return string(*f.Reason)
}
