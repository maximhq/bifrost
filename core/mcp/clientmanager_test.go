package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestCreateSTDIOConnectionAllowsInlineEnvAssignments(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_ASSIGNMENT=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionAllowsSetReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_SET", "set-value")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_SET"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionRequiresReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_MISSING", "")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_MISSING"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable TEST_STDIO_ENV_REFERENCE_MISSING is not set")
}

func TestCreateSTDIOConnectionRejectsEmptyEnvAssignmentName(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable name is empty")
}
