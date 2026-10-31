// Package apispec embeds the OpenAPI document so the server can serve it.
package apispec

import _ "embed"

// Spec is the OpenAPI 3.0 description of the REST API.
//
//go:embed openapi.yaml
var Spec []byte
