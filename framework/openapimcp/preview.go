package openapimcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ResolveBaseURL decides the upstream base: an explicit override wins, else the
// spec's first absolute server, else a relative server resolved against the
// spec URL. explicit reports whether the admin override was used (op-level
// servers are then ignored by the executor).
func ResolveBaseURL(doc *Document, override *string, specURL string) (base string, explicit bool, err error) {
	if override != nil && strings.TrimSpace(*override) != "" {
		base = strings.TrimSpace(*override)
		if !isAbsoluteHTTPURL(base) {
			return "", false, errors.New("openapi_config.base_url must be an absolute http(s) URL")
		}
		return strings.TrimSuffix(base, "/"), true, nil
	}
	if doc != nil {
		for _, srv := range doc.Servers {
			if isAbsoluteHTTPURL(srv) {
				return strings.TrimSuffix(srv, "/"), false, nil
			}
			if resolved := resolveRelativeURL(srv, doc.Self, specURL); isAbsoluteHTTPURL(resolved) {
				return strings.TrimSuffix(resolved, "/"), false, nil
			}
		}
	}
	return "", false, errors.New("the spec declares no absolute server URL; set openapi_config.base_url")
}

// SpecHash is the stable identity of a spec's bytes.
func SpecHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Preview parses and synthesizes without building a server, for the UI's
// parse-first flow and for validating a create/update request.
func Preview(data []byte, parseOpts ParseOptions, synthOpts SynthesizeOptions, baseURLOverride *string) (*PreviewResult, *Synthesis, error) {
	doc, err := Parse(data, parseOpts)
	if err != nil {
		return nil, nil, err
	}
	syn, err := Synthesize(doc, synthOpts)
	if err != nil {
		return nil, nil, err
	}
	result := &PreviewResult{
		Title:           doc.Title,
		Version:         doc.Version,
		OpenAPIVersion:  doc.OpenAPIVersion,
		Description:     doc.Description,
		Servers:         append([]string{}, doc.Servers...),
		SecuritySchemes: make([]SecurityScheme, 0, len(doc.SecuritySchemes)),
		Tools:           make([]ToolSummary, 0, len(syn.Tools)),
		Unsupported:     append([]UnsupportedOperation{}, syn.Unsupported...),
		Warnings:        append([]string{}, syn.Warnings...),
		ToolCount:       len(syn.Tools),
		SpecSize:        len(data),
		SpecHash:        SpecHash(data),
	}
	if base, _, err := ResolveBaseURL(doc, baseURLOverride, parseOpts.SpecURL); err == nil {
		result.BaseURL = base
	} else {
		result.Warnings = append(result.Warnings, err.Error())
	}
	for _, name := range sortedSchemeNames(doc.SecuritySchemes) {
		result.SecuritySchemes = append(result.SecuritySchemes, doc.SecuritySchemes[name])
	}
	for _, t := range syn.Tools {
		result.Tools = append(result.Tools, ToolSummary{
			Name:        t.Name,
			OperationID: t.Operation.ID,
			Method:      t.Operation.Method,
			Path:        t.Operation.Path,
			Summary:     t.Operation.Summary,
			Description: t.Description,
			Deprecated:  t.Operation.Deprecated,
			InputSchema: t.InputSchema,
			Annotations: t.Annotations,
			Warnings:    t.Warnings,
		})
	}
	return result, syn, nil
}

// ToolNames lists the synthesized tool names, for validating tools_to_execute.
func (s *Synthesis) ToolNames() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.Tools))
	for _, t := range s.Tools {
		names = append(names, t.Name)
	}
	return names
}

// ApplyMetadata fills the server-computed fields of an openapi_config from a
// parsed spec so stored rows describe themselves without re-parsing.
func ApplyMetadata(cfg *schemas.MCPOpenAPIConfig, data []byte, doc *Document, syn *Synthesis) {
	if cfg == nil {
		return
	}
	cfg.SpecSize = len(data)
	cfg.SpecHash = SpecHash(data)
	if doc != nil {
		cfg.SpecTitle = doc.Title
		cfg.OpenAPIVersion = doc.OpenAPIVersion
	}
	if syn != nil {
		cfg.OperationCount = len(syn.Tools)
	}
}

func sortedSchemeNames(m map[string]SecurityScheme) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
