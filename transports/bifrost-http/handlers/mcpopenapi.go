package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// openAPISpecFetchTimeout bounds a spec_url download made on a caller's behalf.
const openAPISpecFetchTimeout = 15 * time.Second

// MCPOpenAPIPreviewRequest is the body of POST /api/mcp/openapi/preview.
type MCPOpenAPIPreviewRequest struct {
	Spec              string `json:"spec,omitempty"`               // Inline document (JSON or YAML)
	SpecURL           string `json:"spec_url,omitempty"`           // Fetched when spec is empty; otherwise resolves relative servers
	BaseURL           string `json:"base_url,omitempty"`           // Optional upstream override to preview with
	IncludeDeprecated bool   `json:"include_deprecated,omitempty"` // Expose deprecated operations
	ClientName        string `json:"client_name,omitempty"`        // Name the client will have, so tool names are cut as create cuts them
}

// MCPOpenAPIPreviewResponse is the parse-only result the UI builds its
// operation picker from. Spec carries the fetched document when the request
// named a spec_url, so the client can store it inline on create, and SpecURL
// the URL it was finally read from after redirects, which its relative servers
// resolve against.
type MCPOpenAPIPreviewResponse struct {
	openapimcp.PreviewResult
	Spec    string `json:"spec,omitempty"`
	SpecURL string `json:"spec_url,omitempty"`
}

// previewOpenAPISpec handles POST /api/mcp/openapi/preview: parse and
// synthesize without creating anything. Same middleware chain and RBAC as
// creating a client.
func (h *MCPHandler) previewOpenAPISpec(ctx *fasthttp.RequestCtx) {
	var req MCPOpenAPIPreviewRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	spec := strings.TrimSpace(req.Spec)
	specURL := strings.TrimSpace(req.SpecURL)
	if spec == "" && specURL == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "spec or spec_url is required")
		return
	}
	var data []byte
	fetched := false
	if spec != "" {
		var err error
		if data, err = openapimcp.LoadBytes([]byte(spec)); err != nil {
			SendError(ctx, openAPISpecErrorStatus(err), fmt.Sprintf("Invalid OpenAPI spec: %v", err))
			return
		}
	} else {
		var status int
		var err error
		if data, specURL, status, err = fetchOpenAPISpecForCaller(ctx, specURL); err != nil {
			SendError(ctx, status, err.Error())
			return
		}
		fetched = true
	}
	var baseOverride *string
	if b := strings.TrimSpace(req.BaseURL); b != "" {
		baseOverride = &b
	}
	result, _, err := openapimcp.Preview(data, openapimcp.ParseOptions{SpecURL: specURL}, openapimcp.SynthesizeOptions{ClientName: strings.TrimSpace(req.ClientName), IncludeDeprecated: req.IncludeDeprecated}, baseOverride)
	if err != nil {
		SendError(ctx, openAPISpecErrorStatus(err), fmt.Sprintf("Invalid OpenAPI spec: %v", err))
		return
	}
	resp := MCPOpenAPIPreviewResponse{PreviewResult: *result}
	if fetched {
		resp.Spec = string(data)
		resp.SpecURL = specURL
	}
	SendJSON(ctx, resp)
}

// getMCPClientOpenAPISpec handles GET /api/mcp/client/{id}/openapi-spec: the
// stored document of an openapi client, which GET /api/mcp/clients leaves out.
func (h *MCPHandler) getMCPClientOpenAPISpec(ctx *fasthttp.RequestCtx) {
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}
	// Copied out under the MCP config lock: a concurrent update replaces the
	// block this reads.
	var spec string
	var isOpenAPI bool
	if h.store != nil {
		spec, isOpenAPI, _ = h.store.GetMCPClientOpenAPISpec(id)
	}
	if !isOpenAPI {
		SendError(ctx, fasthttp.StatusNotFound, "OpenAPI MCP client not found")
		return
	}
	if strings.TrimSpace(spec) == "" {
		SendError(ctx, fasthttp.StatusNotFound, "This client has no stored spec (it is fetched from spec_url at connect time)")
		return
	}
	contentType := "application/yaml; charset=utf-8"
	if trimmed := strings.TrimSpace(spec); strings.HasPrefix(trimmed, "{") {
		contentType = "application/json; charset=utf-8"
	}
	ctx.SetContentType(contentType)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBodyString(spec)
}

// fetchOpenAPISpecForCaller downloads spec_url on the caller's behalf with the
// same network posture connection_string gets: a caller let through with no
// credential check (auth bypassed) may only reach public addresses, and the
// download itself goes through the SSRF-safe client; an authenticated admin may
// point at private hosts. Returns the document's final URL after redirects
// (what relative servers resolve against) and, on error, the HTTP status to
// answer with.
func fetchOpenAPISpecForCaller(ctx *fasthttp.RequestCtx, specURL string) ([]byte, string, int, error) {
	authBypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool)
	var client *http.Client
	if authBypassed {
		if msg, refused := privateTargetRefusal(specURL); refused {
			return nil, "", fasthttp.StatusForbidden, errors.New(msg)
		}
		client = network.NewSSRFSafeHTTPClient(openAPISpecFetchTimeout)
	} else {
		client = network.NewPrivateNetworkHTTPClient(openAPISpecFetchTimeout)
	}
	fetchCtx, cancel := context.WithTimeout(context.Background(), openAPISpecFetchTimeout)
	defer cancel()
	data, finalURL, err := openapimcp.LoadURL(fetchCtx, client, specURL)
	if err != nil {
		return nil, "", openAPISpecErrorStatus(err), fmt.Errorf("could not load spec_url: %w", err)
	}
	return data, finalURL, 0, nil
}

func openAPISpecErrorStatus(err error) int {
	if errors.Is(err, openapimcp.ErrSpecTooLarge) {
		return fasthttp.StatusRequestEntityTooLarge
	}
	return fasthttp.StatusBadRequest
}

// preparedOpenAPIClient is what the create/update paths need from a validated
// openapi_config: the normalized block (spec inlined, metadata filled) and the
// synthesized tool names tools_to_execute is checked against.
type preparedOpenAPIClient struct {
	config    *schemas.MCPOpenAPIConfig
	toolNames map[string]bool
	baseURL   string
}

// requestSpecFileRefusal refuses a spec_file sent over the API. A stored
// spec_file is fine: config.json clients keep it as provenance next to the
// document loaded from it, so only the request's own block is checked.
func requestSpecFileRefusal(requested *schemas.MCPOpenAPIConfig) (string, bool) {
	if requested != nil && requested.SpecFile != nil && strings.TrimSpace(*requested.SpecFile) != "" {
		return "openapi_config.spec_file is only supported in config.json; send the document as spec or spec_url", true
	}
	return "", false
}

// prepareOpenAPIConfig validates an openapi_config arriving over the API,
// inlines a spec_url document so the stored row carries the spec, resolves the
// upstream base URL (subject to the public-target rule for unauthenticated
// callers), and synthesizes the tool set. status is 0 on success. It writes
// into cfg, so callers pass a block they own, never the live one.
func prepareOpenAPIConfig(ctx *fasthttp.RequestCtx, clientName string, cfg *schemas.MCPOpenAPIConfig) (*preparedOpenAPIClient, int, string) {
	if cfg == nil {
		return nil, fasthttp.StatusBadRequest, "openapi_config is required for connection_type 'openapi'"
	}
	if cfg.MaxResponseBytes < 0 {
		return nil, fasthttp.StatusBadRequest, "openapi_config.max_response_bytes must not be negative"
	}
	specURL := ""
	if cfg.SpecURL != nil {
		specURL = strings.TrimSpace(*cfg.SpecURL)
	}
	var data []byte
	if strings.TrimSpace(cfg.Spec) != "" {
		var err error
		if data, err = openapimcp.LoadBytes([]byte(cfg.Spec)); err != nil {
			return nil, openAPISpecErrorStatus(err), fmt.Sprintf("Invalid openapi_config.spec: %v", err)
		}
	} else if specURL != "" {
		var status int
		var err error
		if data, specURL, status, err = fetchOpenAPISpecForCaller(ctx, specURL); err != nil {
			return nil, status, err.Error()
		}
		// The document is stored inline; its final URL (after redirects) is
		// kept as spec_url so relative servers resolve the same way on connect.
		cfg.Spec = string(data)
		cfg.SpecURL = &specURL
	} else {
		return nil, fasthttp.StatusBadRequest, "openapi_config requires spec (inline document) or spec_url"
	}
	var baseOverride *string
	if cfg.BaseURL != nil {
		trimmed := strings.TrimRight(strings.TrimSpace(*cfg.BaseURL), "/")
		if trimmed == "" {
			cfg.BaseURL = nil
		} else {
			cfg.BaseURL = &trimmed
			baseOverride = &trimmed
		}
	}
	result, syn, err := openapimcp.Preview(data, openapimcp.ParseOptions{SpecURL: specURL}, openapimcp.SynthesizeOptions{ClientName: clientName, IncludeDeprecated: cfg.IncludeDeprecated}, baseOverride)
	if err != nil {
		return nil, openAPISpecErrorStatus(err), fmt.Sprintf("Invalid openapi_config.spec: %v", err)
	}
	if len(syn.Tools) == 0 {
		reasons := make([]string, 0, len(syn.Unsupported))
		for _, u := range syn.Unsupported {
			reasons = append(reasons, fmt.Sprintf("%s %s: %s", u.Method, u.Path, u.Reason))
		}
		return nil, fasthttp.StatusBadRequest, "No operation in the spec can be exposed as a tool: " + strings.Join(reasons, "; ")
	}
	if result.BaseURL == "" && !result.OperationServers {
		return nil, fasthttp.StatusBadRequest, "The spec declares no absolute server URL; set openapi_config.base_url"
	}
	if authBypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); authBypassed {
		// Unless an explicit base_url overrides them, operations that declare
		// their own server send their calls there, so each of those gets the
		// public-target check the base URL does.
		var targets []string
		if result.BaseURL != "" {
			targets = append(targets, result.BaseURL)
		}
		if baseOverride == nil {
			for _, tool := range syn.Tools {
				if len(tool.Operation.Servers) > 0 && isAbsoluteHTTPURL(tool.Operation.Servers[0]) {
					targets = append(targets, tool.Operation.Servers[0])
				}
			}
		}
		for _, target := range targets {
			if msg, refused := privateTargetRefusal(target); refused {
				return nil, fasthttp.StatusForbidden, msg
			}
		}
	}
	openapimcp.ApplyMetadata(cfg, data, syn.Document, syn)
	names := make(map[string]bool, len(syn.Tools))
	for _, name := range syn.ToolNames() {
		names[name] = true
	}
	return &preparedOpenAPIClient{config: cfg, toolNames: names, baseURL: result.BaseURL}, 0, ""
}

// unknownOpenAPITools lists entries of a tool allow-list that the synthesized
// server does not offer ("*" aside), so a typo is a 400 instead of a silently
// empty tool set.
func unknownOpenAPITools(list schemas.WhiteList, names map[string]bool) []string {
	var unknown []string
	for _, entry := range list {
		if entry == "*" || names[entry] {
			continue
		}
		unknown = append(unknown, entry)
	}
	sort.Strings(unknown)
	return unknown
}

// openAPICredentialSecretReferences lists env./vault. references inside
// openapi_config.security_credentials by wire name.
func openAPICredentialSecretReferences(cfg *schemas.MCPOpenAPIConfig) []string {
	if cfg == nil {
		return nil
	}
	isRef := func(v *schemas.SecretVar) bool { return v != nil && v.Type() != schemas.SecretTypePlainText }
	var refs []string
	for name, cred := range cfg.SecurityCredentials {
		prefix := "openapi_config.security_credentials." + name
		if isRef(cred.Value) {
			refs = append(refs, prefix+".value")
		}
		if isRef(cred.Username) {
			refs = append(refs, prefix+".username")
		}
		if isRef(cred.Password) {
			refs = append(refs, prefix+".password")
		}
	}
	sort.Strings(refs)
	return refs
}

// mergeOpenAPIConfigUpdate applies PATCH semantics to an openapi_config
// update: fields the caller left out keep the stored value, and a masked
// credential round-tripped from a GET keeps the stored secret. An env/vault
// reference is an intentional change and is written.
// openAPIOptionFields records which openapi_config options an update request
// actually carried, since false and 0 cannot be told apart from omitted once
// decoded.
type openAPIOptionFields struct {
	includeDeprecated bool
	maxResponseBytes  bool
}

// isAbsoluteHTTPURL reports whether s is an absolute http(s) URL, the only
// kind of operation server the executor sends calls to.
func isAbsoluteHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// openAPIOptionsSent reads which options an update body sets.
func openAPIOptionsSent(body []byte) openAPIOptionFields {
	return openAPIOptionFields{
		includeDeprecated: gjson.GetBytes(body, "openapi_config.include_deprecated").Exists(),
		maxResponseBytes:  gjson.GetBytes(body, "openapi_config.max_response_bytes").Exists(),
	}
}

func mergeOpenAPIConfigUpdate(existing, incoming *schemas.MCPOpenAPIConfig, sent openAPIOptionFields) *schemas.MCPOpenAPIConfig {
	merged := schemas.MCPOpenAPIConfig{}
	if existing != nil {
		merged = *existing
	}
	if incoming == nil {
		return &merged
	}
	if strings.TrimSpace(incoming.Spec) != "" {
		merged.Spec = incoming.Spec
	}
	// A spec_url sent back in its masked read form keeps the stored URL, like a
	// masked credential keeps the stored secret.
	incomingSpecURL := incoming.SpecURL
	if incomingSpecURL != nil && existing != nil && existing.SpecURL != nil &&
		strings.TrimSpace(*incomingSpecURL) != *existing.SpecURL &&
		strings.TrimSpace(*incomingSpecURL) == lib.RedactURLCredentials(*existing.SpecURL) {
		incomingSpecURL = nil
	}
	if incomingSpecURL != nil {
		trimmed := strings.TrimSpace(*incomingSpecURL)
		if trimmed == "" {
			merged.SpecURL = nil
		} else {
			merged.SpecURL = &trimmed
			if existing == nil || existing.SpecURL == nil || *existing.SpecURL != trimmed {
				// A new source: the stored document no longer describes it.
				if strings.TrimSpace(incoming.Spec) == "" {
					merged.Spec = ""
				}
			}
		}
	}
	// A base_url sent back in its masked read form keeps the stored URL too.
	if incoming.BaseURL != nil && !(existing != nil && existing.BaseURL != nil &&
		strings.TrimSpace(*incoming.BaseURL) != *existing.BaseURL &&
		strings.TrimSpace(*incoming.BaseURL) == lib.RedactURLCredentials(*existing.BaseURL)) {
		merged.BaseURL = incoming.BaseURL
	}
	if sent.includeDeprecated {
		merged.IncludeDeprecated = incoming.IncludeDeprecated
	}
	if sent.maxResponseBytes {
		merged.MaxResponseBytes = incoming.MaxResponseBytes
	}
	if incoming.SecurityCredentials != nil {
		creds := make(map[string]schemas.MCPOpenAPICredential, len(incoming.SecurityCredentials))
		for name, cred := range incoming.SecurityCredentials {
			var stored schemas.MCPOpenAPICredential
			if existing != nil {
				stored = existing.SecurityCredentials[name]
			}
			creds[name] = schemas.MCPOpenAPICredential{
				Value:    preserveStoredSecret(cred.Value, stored.Value),
				Username: preserveStoredSecret(cred.Username, stored.Username),
				Password: preserveStoredSecret(cred.Password, stored.Password),
			}
		}
		merged.SecurityCredentials = creds
	}
	return &merged
}

func openAPIStringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// openAPITargetChanged reports whether an update changes where an OpenAPI
// client's calls go: its document (and with it the operation servers), the
// URL it is fetched from, or the base URL override.
func openAPITargetChanged(existing, merged *schemas.MCPOpenAPIConfig) bool {
	var old schemas.MCPOpenAPIConfig
	if existing != nil {
		old = *existing
	}
	base := func(s *string) string { return strings.TrimRight(strings.TrimSpace(openAPIStringValue(s)), "/") }
	return old.Spec != merged.Spec || strings.TrimSpace(openAPIStringValue(old.SpecURL)) != strings.TrimSpace(openAPIStringValue(merged.SpecURL)) || base(old.BaseURL) != base(merged.BaseURL)
}

// openAPIRetargetCarriesStoredSecrets reports whether a retargeted OpenAPI
// client would send secrets stored for its old target to the new one: a
// security credential or static header kept from storage (omitted or sent
// back redacted), or the per-user tokens of a per_user_headers client.
func openAPIRetargetCarriesStoredSecrets(existing *schemas.MCPClientConfig, merged *schemas.MCPOpenAPIConfig, headers map[string]schemas.SecretVar) bool {
	if existing.AuthType == schemas.MCPAuthTypePerUserHeaders {
		return true
	}
	for name, stored := range existing.Headers {
		if current, ok := headers[name]; ok && stored.GetValue() != "" && current.Equals(&stored) {
			return true
		}
	}
	if existing.OpenAPIConfig == nil {
		return false
	}
	kept := func(current, stored *schemas.SecretVar) bool {
		return stored != nil && stored.GetValue() != "" && current != nil && current.Equals(stored)
	}
	for name, stored := range existing.OpenAPIConfig.SecurityCredentials {
		current, ok := merged.SecurityCredentials[name]
		if !ok {
			continue
		}
		if kept(current.Value, stored.Value) || kept(current.Username, stored.Username) || kept(current.Password, stored.Password) {
			return true
		}
	}
	return false
}

func preserveStoredSecret(incoming, stored *schemas.SecretVar) *schemas.SecretVar {
	if incoming == nil {
		return stored
	}
	if incoming.ShouldPreserveStored() && stored != nil {
		return stored
	}
	return incoming
}
