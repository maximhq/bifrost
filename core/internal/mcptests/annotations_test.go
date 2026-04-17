package mcptests

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// MCP ANNOTATION TESTS
//
// These tests verify two invariants of the MCP annotations feature:
//
//  1. PRESERVATION: annotations attached to a registered tool survive the full
//     MCP→Bifrost conversion and remain accessible on ChatTool.Annotations
//     after retrieval from the manager.
//
//  2. ISOLATION: annotations are tagged json:"-" on ChatTool, so they are never
//     included in the JSON body forwarded to LLM providers.
// =============================================================================

// TestAnnotations_PreservedAfterToolRegistration verifies that annotations set
// on an InProcess ChatTool schema are stored in the tool map without modification.
func TestAnnotations_PreservedAfterToolRegistration(t *testing.T) {
	t.Parallel()

	readOnly := true
	idempotent := true

	manager := setupMCPManager(t)

	toolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "read_resource",
			Description: schemas.Ptr("Reads a resource"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("uri", map[string]interface{}{
						"type":        "string",
						"description": "URI of the resource to read",
					}),
				),
				Required: []string{"uri"},
			},
		},
		Annotations: &schemas.MCPToolAnnotations{
			Title:          "Resource Reader",
			ReadOnlyHint:   &readOnly,
			IdempotentHint: &idempotent,
		},
	}

	err := manager.RegisterTool(
		"read_resource",
		"Reads a resource",
		func(args any) (string, error) { return `{"ok":true}`, nil },
		toolSchema,
	)
	require.NoError(t, err)

	ctx := createTestContext()
	toolPerClient := manager.GetToolPerClient(ctx)

	var found *schemas.ChatTool
outer1:
	for _, tools := range toolPerClient {
		for i := range tools {
			if tools[i].Function != nil && strings.HasSuffix(tools[i].Function.Name, "-read_resource") {
				cp := tools[i]
				found = &cp
				break outer1
			}
		}
	}
	require.NotNil(t, found, "read_resource tool should be present in the tool map")

	// Annotations must be preserved on ChatTool (not lost after registration)
	require.NotNil(t, found.Annotations, "Annotations should be preserved on ChatTool")
	assert.Equal(t, "Resource Reader", found.Annotations.Title)
	require.NotNil(t, found.Annotations.ReadOnlyHint)
	assert.True(t, *found.Annotations.ReadOnlyHint)
	require.NotNil(t, found.Annotations.IdempotentHint)
	assert.True(t, *found.Annotations.IdempotentHint)
	assert.Nil(t, found.Annotations.DestructiveHint)
	assert.Nil(t, found.Annotations.OpenWorldHint)
}

// TestAnnotations_AbsentFromProviderJSON verifies that annotations do NOT appear
// in the JSON representation of a tool — i.e. the payload that would be forwarded
// to an LLM provider.
func TestAnnotations_AbsentFromProviderJSON(t *testing.T) {
	t.Parallel()

	readOnly := true
	destructive := false

	manager := setupMCPManager(t)

	toolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "write_file",
			Description: schemas.Ptr("Writes content to a file"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("path", map[string]interface{}{
						"type":        "string",
						"description": "Destination file path",
					}),
					schemas.KV("content", map[string]interface{}{
						"type":        "string",
						"description": "Content to write",
					}),
				),
				Required: []string{"path", "content"},
			},
		},
		Annotations: &schemas.MCPToolAnnotations{
			Title:           "File Writer",
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
		},
	}

	err := manager.RegisterTool(
		"write_file",
		"Writes content to a file",
		func(args any) (string, error) { return `{"ok":true}`, nil },
		toolSchema,
	)
	require.NoError(t, err)

	ctx := createTestContext()
	toolPerClient := manager.GetToolPerClient(ctx)

	var found *schemas.ChatTool
outer2:
	for _, tools := range toolPerClient {
		for i := range tools {
			if tools[i].Function != nil && strings.HasSuffix(tools[i].Function.Name, "-write_file") {
				cp := tools[i]
				found = &cp
				break outer2
			}
		}
	}
	require.NotNil(t, found, "write_file tool should be present in the tool map")

	// The tool must have annotations in memory
	require.NotNil(t, found.Annotations, "Annotations must be in memory for downstream use")

	// Serialize the tool as a provider would receive it
	toolJSON, err := json.Marshal(found)
	require.NoError(t, err)
	s := string(toolJSON)

	// None of the annotation data must leak into the JSON.
	// Use the key token `"annotations":` to avoid false positives from description text.
	assert.NotContains(t, s, `"annotations":`, "annotations key must be absent from provider JSON")
	assert.NotContains(t, s, "readOnlyHint", "readOnlyHint must be absent from provider JSON")
	assert.NotContains(t, s, "destructiveHint", "destructiveHint must be absent from provider JSON")
	assert.NotContains(t, s, "File Writer", "annotation title must be absent from provider JSON")

	// The function definition itself must still be present
	assert.Contains(t, s, "write_file", "function name must be present in provider JSON")
	assert.Contains(t, s, "path", "parameter must be present in provider JSON")
}

// TestAnnotations_DeepCopyPreservesAnnotations verifies that the deep-copy path
// (used during plugin accumulation and streaming) correctly copies annotations.
func TestAnnotations_DeepCopyPreservesAnnotations(t *testing.T) {
	t.Parallel()

	readOnly := true

	original := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "read_config",
			Description: schemas.Ptr("Reads configuration from disk"),
		},
		Annotations: &schemas.MCPToolAnnotations{
			Title:        "Config Reader",
			ReadOnlyHint: &readOnly,
		},
	}

	copied := schemas.DeepCopyChatTool(original)

	// Annotations must survive the deep copy
	require.NotNil(t, copied.Annotations, "Annotations must be preserved after deep copy")
	assert.Equal(t, "Config Reader", copied.Annotations.Title)
	require.NotNil(t, copied.Annotations.ReadOnlyHint)
	assert.True(t, *copied.Annotations.ReadOnlyHint)

	// Mutate via the pointed-to value to detect pointer aliasing
	*original.Annotations.ReadOnlyHint = false
	assert.NotSame(t, original.Annotations.ReadOnlyHint, copied.Annotations.ReadOnlyHint,
		"deep copy must not share the ReadOnlyHint pointer with the original")
	assert.True(t, *copied.Annotations.ReadOnlyHint,
		"mutating original's ReadOnlyHint must not affect the deep copy")

	// JSON of the copy must also be annotation-free (same guarantee as the original)
	toolJSON, err := json.Marshal(copied)
	require.NoError(t, err)
	s := string(toolJSON)
	// Check for the JSON key pattern, not just the substring, to avoid false positives
	// from description text. The key would appear as `"annotations":` in JSON.
	assert.NotContains(t, s, `"annotations":`,
		"annotations key must be absent from provider JSON even after deep copy")
	assert.NotContains(t, s, "readOnlyHint",
		"readOnlyHint must be absent from provider JSON even after deep copy")
}
