package schemas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// MarshalExactCanonicalJSON normalizes JSON object key order while preserving
// integer precision. Decoder.UseNumber keeps integer tokens as json.Number so
// the round trip never passes large JSON integers through float64.
func MarshalExactCanonicalJSON(v any) ([]byte, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var plain any
	if err := decoder.Decode(&plain); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON")
		}
		return nil, err
	}
	canonical, err := json.Marshal(plain)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// MCPToolSchema is the original MCP contract, separate from the provider-facing
// parameter projection. Nil means absent; a non-nil {} is an explicit schema.
// ChatTool excludes this metadata from provider JSON.
type MCPToolSchema struct {
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// Clone owns its schema bytes, avoiding mutation of cached or upstream data.
func (s *MCPToolSchema) Clone() *MCPToolSchema {
	if s == nil {
		return nil
	}
	return &MCPToolSchema{InputSchema: append(json.RawMessage(nil), s.InputSchema...), OutputSchema: append(json.RawMessage(nil), s.OutputSchema...)}
}

// Clone owns the annotation and each optional boolean.
func (a *MCPToolAnnotations) Clone() *MCPToolAnnotations {
	if a == nil {
		return nil
	}
	clone := *a
	copyBool := func(b *bool) *bool {
		if b == nil {
			return nil
		}
		v := *b
		return &v
	}
	clone.ReadOnlyHint = copyBool(a.ReadOnlyHint)
	clone.DestructiveHint = copyBool(a.DestructiveHint)
	clone.IdempotentHint = copyBool(a.IdempotentHint)
	clone.OpenWorldHint = copyBool(a.OpenWorldHint)
	return &clone
}

// mcpStoredMetadata is private to discovered_tools_json. Version 2 stores the
// raw native definition and its projection fingerprint beside the provider view.
type mcpStoredMetadata struct {
	Version                         int                 `json:"version"`
	Schema                          *MCPToolSchema      `json:"schema,omitempty"`
	Annotations                     *MCPToolAnnotations `json:"annotations,omitempty"`
	SDKRawToolJSON                  json.RawMessage     `json:"sdkRawToolJSON,omitempty"`
	SDKRawToolProjectionFingerprint string              `json:"sdkRawToolProjectionFingerprint,omitempty"`
}

const mcpStorageField = "_bifrost_mcp"

func validateMCPStoredSchema(s *MCPToolSchema) error {
	if s == nil {
		return nil
	}
	for _, raw := range []json.RawMessage{s.InputSchema, s.OutputSchema} {
		if raw == nil {
			continue
		}
		trimmed := bytes.TrimSpace(raw)
		if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return fmt.Errorf("MCP stored schema must be a JSON object or boolean")
		}
	}
	return nil
}

func validateSDKRawToolJSON(raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("stored raw MCP tool must be a JSON object")
	}
	return nil
}

func validateSDKRawToolProjectionFingerprint(fingerprint string) error {
	if len(fingerprint) != sha256.Size*2 {
		return fmt.Errorf("invalid raw MCP tool projection fingerprint")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return fmt.Errorf("invalid raw MCP tool projection fingerprint")
	}
	return nil
}

type sdkRawToolProjection struct {
	Type               ChatToolType            `json:"type"`
	FunctionPresent    bool                    `json:"function_present"`
	DescriptionPresent bool                    `json:"description_present"`
	Description        *string                 `json:"description,omitempty"`
	ParametersPresent  bool                    `json:"parameters_present"`
	Parameters         *ToolFunctionParameters `json:"parameters,omitempty"`
	StrictPresent      bool                    `json:"strict_present"`
	Strict             *bool                   `json:"strict,omitempty"`
	SchemaPresent      bool                    `json:"schema_present"`
	Schema             *MCPToolSchema          `json:"schema,omitempty"`
	AnnotationsPresent bool                    `json:"annotations_present"`
	Annotations        *MCPToolAnnotations     `json:"annotations,omitempty"`
}

// SDKRawToolProjectionFingerprint covers the provider projection that native
// MCP tools/list reconstructs. Function.Name is intentionally excluded because
// Bifrost owns namespacing; edits to type, description, parameters, source
// schemas, strictness, or typed annotations invalidate the raw carrier.
func SDKRawToolProjectionFingerprint(tool ChatTool) string {
	projection := sdkRawToolProjection{Type: tool.Type, FunctionPresent: tool.Function != nil}
	if tool.Function != nil {
		projection.DescriptionPresent = tool.Function.Description != nil
		projection.Description = tool.Function.Description
		projection.ParametersPresent = tool.Function.Parameters != nil
		projection.Parameters = tool.Function.Parameters
		projection.StrictPresent = tool.Function.Strict != nil
		projection.Strict = tool.Function.Strict
	}
	projection.SchemaPresent = tool.MCPToolSchema != nil
	projection.Schema = tool.MCPToolSchema
	projection.AnnotationsPresent = tool.Annotations != nil
	projection.Annotations = tool.Annotations
	data, err := MarshalExactCanonicalJSON(projection)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (tool ChatTool) HasSDKRawToolProjectionConflict() bool {
	if tool.SDKRawToolJSON == nil {
		return false
	}
	if validateSDKRawToolProjectionFingerprint(tool.SDKRawToolProjectionFingerprint) != nil {
		return true
	}
	return SDKRawToolProjectionFingerprint(tool) != tool.SDKRawToolProjectionFingerprint
}

// MarshalMCPDiscoveredTools stores MCP metadata without changing ChatTool's
// provider serializer or its cached bytes. Nil and empty maps remain distinct.
func MarshalMCPDiscoveredTools(tools map[string]ChatTool) ([]byte, error) {
	if tools == nil {
		return []byte("null"), nil
	}
	stored := make(map[string]json.RawMessage, len(tools))
	for name, tool := range tools {
		if err := validateMCPStoredSchema(tool.MCPToolSchema); err != nil {
			return nil, fmt.Errorf("tool %q: %w", name, err)
		}
		if err := validateSDKRawToolJSON(tool.SDKRawToolJSON); err != nil {
			return nil, fmt.Errorf("tool %q: %w", name, err)
		}
		projection, err := json.Marshal(tool)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(projection, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("tool %q: invalid provider projection", name)
		}
		if tool.MCPToolSchema != nil || tool.Annotations != nil || tool.SDKRawToolJSON != nil {
			version := 1
			metadataValue := mcpStoredMetadata{
				Version:     version,
				Schema:      tool.MCPToolSchema.Clone(),
				Annotations: tool.Annotations.Clone(),
			}
			if tool.SDKRawToolJSON != nil {
				if err := validateSDKRawToolProjectionFingerprint(tool.SDKRawToolProjectionFingerprint); err != nil {
					return nil, fmt.Errorf("tool %q: %w", name, err)
				}
				metadataValue.Version = 2
				metadataValue.SDKRawToolJSON = append(json.RawMessage(nil), tool.SDKRawToolJSON...)
				metadataValue.SDKRawToolProjectionFingerprint = tool.SDKRawToolProjectionFingerprint
			}
			metadata, err := json.Marshal(metadataValue)
			if err != nil {
				return nil, err
			}
			fields[mcpStorageField] = metadata
		}
		stored[name], err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(stored)
}

// UnmarshalMCPDiscoveredTools reads both old projections and new records. Old
// records retain nil metadata: rediscovery is required for a complete contract.
// Unsupported or damaged metadata fails instead of silently becoming legacy.
func UnmarshalMCPDiscoveredTools(data []byte) (map[string]ChatTool, error) {
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, nil
	}
	tools := make(map[string]ChatTool, len(stored))
	for name, raw := range stored {
		var tool ChatTool
		if err := json.Unmarshal(raw, &tool); err != nil {
			return nil, fmt.Errorf("tool %q: %w", name, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("tool %q: invalid stored object", name)
		}
		if metadata, present := fields[mcpStorageField]; present {
			var metadataFields map[string]json.RawMessage
			if err := json.Unmarshal(metadata, &metadataFields); err != nil || metadataFields == nil {
				return nil, fmt.Errorf("tool %q: invalid MCP metadata object", name)
			}
			_, hasSchema := metadataFields["schema"]
			_, hasAnnotations := metadataFields["annotations"]
			if !hasSchema && !hasAnnotations {
				var header struct {
					Version int `json:"version"`
				}
				if err := json.Unmarshal(metadata, &header); err != nil || header.Version != 2 {
					return nil, fmt.Errorf("tool %q: empty MCP metadata", name)
				}
			}
			for _, field := range []string{"schema", "annotations"} {
				if bytes.Equal(bytes.TrimSpace(metadataFields[field]), []byte("null")) {
					return nil, fmt.Errorf("tool %q: null MCP metadata %s", name, field)
				}
			}
			var value mcpStoredMetadata
			decoder := json.NewDecoder(bytes.NewReader(metadata))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&value); err != nil {
				return nil, fmt.Errorf("tool %q: invalid MCP metadata: %w", name, err)
			}
			if value.Version != 1 && value.Version != 2 {
				return nil, fmt.Errorf("tool %q: unsupported MCP metadata version %d", name, value.Version)
			}
			_, hasRawToolJSON := metadataFields["sdkRawToolJSON"]
			_, hasFingerprint := metadataFields["sdkRawToolProjectionFingerprint"]
			if value.Version == 1 && (hasRawToolJSON || hasFingerprint) {
				return nil, fmt.Errorf("tool %q: v1 MCP metadata cannot contain raw tool fields", name)
			}
			if value.Version == 2 && (!hasRawToolJSON || !hasFingerprint) {
				return nil, fmt.Errorf("tool %q: v2 MCP metadata requires raw tool and projection fingerprint", name)
			}
			for _, field := range []string{"sdkRawToolJSON", "sdkRawToolProjectionFingerprint"} {
				if bytes.Equal(bytes.TrimSpace(metadataFields[field]), []byte("null")) {
					return nil, fmt.Errorf("tool %q: null MCP metadata %s", name, field)
				}
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return nil, fmt.Errorf("tool %q: trailing MCP metadata", name)
			}
			if err := validateMCPStoredSchema(value.Schema); err != nil {
				return nil, fmt.Errorf("tool %q: %w", name, err)
			}
			if value.Version == 2 {
				if err := validateSDKRawToolJSON(value.SDKRawToolJSON); err != nil {
					return nil, fmt.Errorf("tool %q: %w", name, err)
				}
				if err := validateSDKRawToolProjectionFingerprint(value.SDKRawToolProjectionFingerprint); err != nil {
					return nil, fmt.Errorf("tool %q: %w", name, err)
				}
				tool.SDKRawToolJSON = append(json.RawMessage(nil), value.SDKRawToolJSON...)
				tool.SDKRawToolProjectionFingerprint = value.SDKRawToolProjectionFingerprint
			}
			tool.MCPToolSchema = value.Schema.Clone()
			tool.Annotations = value.Annotations.Clone()
		}
		tools[name] = tool
	}
	return tools, nil
}
