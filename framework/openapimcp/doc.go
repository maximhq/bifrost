// Package openapimcp synthesizes an in-process MCP server from an OpenAPI or
// Swagger document: every operation becomes one MCP tool whose handler issues
// the corresponding HTTP request against the upstream API.
//
// It is the implementation behind the "openapi" MCP connection type. Core calls
// Factory on every connect of such a client (see schemas.MCPConfig.
// InProcessServerFactory) and hands it the HTTP client and header resolver it
// wants the synthesized tools to use; this package never decides credentials or
// network policy on its own.
//
// Supported documents: Swagger 2.0, OpenAPI 3.0.x, 3.1.x and 3.2.x, in JSON or
// YAML. Newer 3.x minors are parsed as 3.2 with a warning. Every version is
// normalized into one Document, so the tool synthesis, request building and
// server construction do not branch on the spec version.
//
// Not supported, and reported per operation in Synthesis.Unsupported rather than
// failing the whole document: multipart and binary request bodies, streaming
// request bodies (3.2 itemSchema), externally referenced schemas, and operations
// whose only security schemes are OAuth2, OpenID Connect or mutual TLS.
//
// Naming: a tool is named after its operationId, sanitized to [A-Za-z0-9_]
// (hyphens would break code mode's tool-name parsing) and capped so that the
// "<client>-<tool>" form the gateway exposes stays within the 64-character
// limit most model providers enforce. Operations without an operationId are
// named "<method>_<path>"; duplicates receive a deterministic numeric suffix.
package openapimcp
