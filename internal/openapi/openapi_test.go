package openapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpecIsValidJSONAndComplete(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(Spec(), &doc); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}

	if got := doc["openapi"]; got != "3.0.3" {
		t.Errorf("openapi version = %v, want 3.0.3", got)
	}

	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] == "" || info["version"] == "" {
		t.Errorf("spec info missing title/version: %v", info)
	}

	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatalf("spec paths missing or not an object")
	}
	for _, p := range []string{"/", "/openapi.json", "/healthz", "/metrics", "/search", "/validate"} {
		if _, exists := paths[p]; !exists {
			t.Errorf("spec missing path %s", p)
		}
	}

	components, ok := doc["components"].(map[string]any)
	if !ok {
		t.Fatalf("spec components missing or not an object")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("spec schemas missing or not an object")
	}
	for _, s := range []string{"SearchRequest", "SearchResponse", "CostReport", "ErrorResponse", "ErrorDetail"} {
		if _, exists := schemas[s]; !exists {
			t.Errorf("spec missing schema %s", s)
		}
	}

	securitySchemes, ok := components["securitySchemes"].(map[string]any)
	if !ok {
		t.Fatalf("spec securitySchemes missing or not an object")
	}
	for _, s := range []string{"apiKey", "bearerAuth"} {
		if _, exists := securitySchemes[s]; !exists {
			t.Errorf("spec missing security scheme %s", s)
		}
	}
}

func TestUIHandlesSpecURL(t *testing.T) {
	html := string(UI())
	if !strings.Contains(html, "/openapi.json") {
		t.Error("UI HTML does not reference /openapi.json")
	}
	if !strings.Contains(html, "swagger-ui") {
		t.Error("UI HTML does not reference swagger-ui")
	}
}
