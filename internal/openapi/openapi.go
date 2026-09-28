// Package openapi embeds the OpenAPI schema and a Swagger UI page that
// presents it. The schema is served as JSON and the UI loads it from the same
// server.
package openapi

import (
	_ "embed"
)

//go:embed openapi.json
var spec []byte

//go:embed index.html
var ui []byte

// Spec returns the embedded OpenAPI JSON document.
func Spec() []byte { return spec }

// UI returns the embedded Swagger UI HTML page.
func UI() []byte { return ui }
