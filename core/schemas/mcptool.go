package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

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

// mcpStoredMetadata is private to discovered_tools_json. The outer object keeps
// the old ChatTool projection, so old readers can ignore this added field.
type mcpStoredMetadata struct {
	Version     int                 `json:"version"`
	Schema      *MCPToolSchema      `json:"schema,omitempty"`
	Annotations *MCPToolAnnotations `json:"annotations,omitempty"`
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
		projection, err := json.Marshal(tool)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(projection, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("tool %q: invalid provider projection", name)
		}
		if tool.MCPToolSchema != nil || tool.Annotations != nil {
			metadata, err := json.Marshal(mcpStoredMetadata{1, tool.MCPToolSchema.Clone(), tool.Annotations.Clone()})
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
				return nil, fmt.Errorf("tool %q: empty MCP metadata", name)
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
			if value.Version != 1 {
				return nil, fmt.Errorf("tool %q: unsupported MCP metadata version %d", name, value.Version)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return nil, fmt.Errorf("tool %q: trailing MCP metadata", name)
			}
			if err := validateMCPStoredSchema(value.Schema); err != nil {
				return nil, fmt.Errorf("tool %q: %w", name, err)
			}
			tool.MCPToolSchema = value.Schema.Clone()
			tool.Annotations = value.Annotations.Clone()
		}
		tools[name] = tool
	}
	return tools, nil
}
