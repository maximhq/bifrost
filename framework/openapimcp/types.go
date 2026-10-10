package openapimcp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// MaxSpecBytes caps a spec document (inline, file or fetched).
	MaxSpecBytes = 5 << 20
	// DefaultMaxResponseBytes caps an upstream response body returned to the model.
	DefaultMaxResponseBytes = 64 << 10
	// ToolCountWarnThreshold is the tool count above which a warning is emitted.
	ToolCountWarnThreshold = 128
	// MaxToolCount is the hard cap on synthesized tools per spec.
	MaxToolCount = 1000
	// MaxToolNameLen is the provider-side cap on a function name, prefix included.
	MaxToolNameLen = 64
	// minToolNameBudget is the floor kept for the operation part of a tool name.
	minToolNameBudget = 16
	// maxDescriptionLen caps the operation description copied into a tool.
	maxDescriptionLen = 1024
	// maxInstructionsLen caps the spec description served as server instructions.
	maxInstructionsLen = 2000
	// maxRefDepth bounds $ref expansion.
	maxRefDepth = 32
)

// Parameter locations. The OpenAPI values plus 3.2's querystring.
const (
	InPath        = "path"
	InQuery       = "query"
	InHeader      = "header"
	InCookie      = "cookie"
	InQuerystring = "querystring"
)

// Request body content types the executor can encode.
const (
	ContentTypeJSON = "application/json"
	ContentTypeForm = "application/x-www-form-urlencoded"
	ContentTypeText = "text/plain"
)

// Document is the version-independent view of a spec.
type Document struct {
	Title           string
	Version         string // info.version
	Description     string
	OpenAPIVersion  string // detected spec version, e.g. "2.0", "3.0.3", "3.1.0"
	Self            string // 3.2 $self, if any
	Servers         []string
	SecuritySchemes map[string]SecurityScheme
	GlobalSecurity  []SecurityRequirement
	Operations      []Operation
	Warnings        []string
}

// SecurityScheme is one components.securitySchemes (or securityDefinitions) entry.
type SecurityScheme struct {
	Name        string `json:"name"`
	Type        string `json:"type"`                 // apiKey | http | oauth2 | openIdConnect | mutualTLS
	In          string `json:"in,omitempty"`         // apiKey: header | query | cookie
	ParamName   string `json:"param_name,omitempty"` // apiKey: header/query/cookie name
	Scheme      string `json:"scheme,omitempty"`     // http: bearer | basic | ...
	Description string `json:"description,omitempty"`
	Supported   bool   `json:"supported"`
	Reason      string `json:"reason,omitempty"` // why unsupported
}

// SecurityRequirement maps scheme names to scopes; all listed schemes apply together.
type SecurityRequirement map[string][]string

// Operation is one HTTP operation of the spec.
type Operation struct {
	ID          string
	Method      string // upper-case HTTP method
	Path        string
	Summary     string
	Description string
	Deprecated  bool
	Tags        []string
	Params      []Parameter
	Body        *RequestBody
	Security    []SecurityRequirement // op-level requirements; meaningful when HasSecurity
	HasSecurity bool                  // op declared its own security (even if empty)
	Servers     []string              // op/path-level server override
	Unsupported string                // non-empty: reason the operation cannot become a tool
}

// Parameter is one path/query/header/cookie/querystring parameter.
type Parameter struct {
	Name        string
	In          string
	Description string
	Required    bool
	Explode     *bool
	Style       string // form | simple | spaceDelimited | pipeDelimited | tabDelimited
	Schema      map[string]any
}

// RequestBody is the selected request body representation of an operation.
type RequestBody struct {
	ContentType string
	Required    bool
	Description string
	Schema      map[string]any
}

// Tool is a synthesized MCP tool plus the bindings needed to execute it.
type Tool struct {
	Name        string
	Operation   Operation
	Description string
	InputSchema json.RawMessage
	Args        []ArgBinding
	Body        *BodyBinding
	Annotations mcp.ToolAnnotation
	Warnings    []string
}

// ArgBinding maps one flattened input property to a wire parameter.
type ArgBinding struct {
	ArgName  string // property name in the tool input schema
	Location string // path | query | header | cookie | querystring
	Name     string // wire parameter name
	Required bool
	Explode  bool
	Style    string
}

// BodyBinding describes how input properties are turned into the request body.
type BodyBinding struct {
	Mode        string   // flattened | single
	ArgNames    []string // flattened: body properties present at the top level
	SingleArg   string   // single: the one property carrying the whole body
	ContentType string
}

// UnsupportedOperation is an operation that did not become a tool.
type UnsupportedOperation struct {
	OperationID string `json:"operation_id,omitempty"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Reason      string `json:"reason"`
}

// Synthesis is the result of turning a Document into tools.
type Synthesis struct {
	Document    *Document
	Tools       []Tool
	Unsupported []UnsupportedOperation
	Warnings    []string
}

// ParseOptions tunes Parse.
type ParseOptions struct {
	MaxBytes int    // 0 = MaxSpecBytes
	SpecURL  string // used to resolve relative server URLs
}

// SynthesizeOptions tunes Synthesize.
type SynthesizeOptions struct {
	ClientName        string // budgets the tool-name length against the "<client>-" prefix
	IncludeDeprecated bool
}

// HeaderResolver supplies the headers for one upstream call.
type HeaderResolver func(ctx context.Context) (http.Header, error)

// ServerOptions configures BuildServer.
type ServerOptions struct {
	ClientName       string
	Synthesis        *Synthesis
	BaseURL          string // resolved upstream base
	BaseURLExplicit  bool   // true when the admin set it; op-level servers are then ignored
	Credentials      map[string]schemas.MCPOpenAPICredential
	HTTPClient       *http.Client
	Headers          HeaderResolver
	MaxResponseBytes int
	Logger           schemas.Logger
}

// PreviewResult is the API-facing summary of a parsed spec.
type PreviewResult struct {
	Title           string                 `json:"title"`
	Version         string                 `json:"version"`
	OpenAPIVersion  string                 `json:"openapi_version"`
	Description     string                 `json:"description,omitempty"`
	Servers         []string               `json:"servers"`
	BaseURL         string                 `json:"base_url,omitempty"`
	SecuritySchemes []SecurityScheme       `json:"security_schemes"`
	Tools           []ToolSummary          `json:"tools"`
	Unsupported     []UnsupportedOperation `json:"unsupported"`
	Warnings        []string               `json:"warnings"`
	ToolCount       int                    `json:"tool_count"`
	SpecSize        int                    `json:"spec_size"`
	SpecHash        string                 `json:"spec_hash"`
}

// ToolSummary is one synthesized tool as shown in a preview.
type ToolSummary struct {
	Name        string             `json:"name"`
	OperationID string             `json:"operation_id,omitempty"`
	Method      string             `json:"method"`
	Path        string             `json:"path"`
	Summary     string             `json:"summary,omitempty"`
	Description string             `json:"description,omitempty"`
	Deprecated  bool               `json:"deprecated,omitempty"`
	InputSchema json.RawMessage    `json:"input_schema"`
	Annotations mcp.ToolAnnotation `json:"annotations"`
	Warnings    []string           `json:"warnings,omitempty"`
}
