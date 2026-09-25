package validate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoOp(t *testing.T) {
	v := NoOp{}
	r, err := v.Validate(context.Background(), "idx", "*:*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Valid {
		t.Error("expected valid")
	}
}

func TestESValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/my-index/_validate/query" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"valid": true,
			"explanations": [
				{"index": "my-index", "valid": true, "explanation": "+asn:AS13335"}
			]
		}`))
	}))
	defer server.Close()

	v, err := NewES(server.URL, false, "", "", "")
	if err != nil {
		t.Fatalf("NewES: %v", err)
	}
	r, err := v.Validate(context.Background(), "my-index", "asn:AS13335")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Valid {
		t.Errorf("expected valid, got error: %s", r.Error)
	}
	if r.Explanation != "+asn:AS13335" {
		t.Errorf("unexpected explanation: %s", r.Explanation)
	}
}

func TestESInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"valid": false,
			"explanations": [
				{"index": "my-index", "valid": false, "explanation": "bad query"}
			]
		}`))
	}))
	defer server.Close()

	v, err := NewES(server.URL, false, "", "", "")
	if err != nil {
		t.Fatalf("NewES: %v", err)
	}
	r, err := v.Validate(context.Background(), "my-index", "bad[[[")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Valid {
		t.Error("expected invalid")
	}
}

func TestESSkipVerify(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"valid": true, "explanations": []}`))
	}))
	defer server.Close()

	v, err := NewES(server.URL, true, "", "", "")
	if err != nil {
		t.Fatalf("NewES: %v", err)
	}
	r, err := v.Validate(context.Background(), "", "asn:AS13335")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Valid {
		t.Errorf("expected valid with insecure skip verify")
	}
}

func TestESBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "es-user" || pass != "es-pass" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`unauthorized`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"valid": true, "explanations": []}`))
	}))
	defer server.Close()

	v, err := NewES(server.URL, false, "", "es-user", "es-pass")
	if err != nil {
		t.Fatalf("NewES: %v", err)
	}
	r, err := v.Validate(context.Background(), "", "asn:AS13335")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Valid {
		t.Errorf("expected valid, got: %s", r.Error)
	}
}

func TestESHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`cluster error`))
	}))
	defer server.Close()

	v, err := NewES(server.URL, false, "", "", "")
	if err != nil {
		t.Fatalf("NewES: %v", err)
	}
	r, err := v.Validate(context.Background(), "my-index", "asn:AS13335")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Valid {
		t.Error("expected invalid on HTTP error")
	}
	if r.Error != "cluster error" {
		t.Errorf("unexpected error body: %s", r.Error)
	}
}
