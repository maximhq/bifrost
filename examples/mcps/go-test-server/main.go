package main

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Create MCP server
	s := server.NewMCPServer(
		"go-test-server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register all tools
	registerStringTransformTool(s)
	registerJSONValidateTool(s)
	registerUUIDGenerateTool(s)
	registerHashTool(s)
	registerEncodeTool(s)
	registerDecodeTool(s)

	// Start STDIO server
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// ============================================================================
// TOOL 1: string_transform
// ============================================================================

func registerStringTransformTool(s *server.MCPServer) {
	tool := mcp.NewTool("string_transform",
		mcp.WithDescription("Performs string transformations: uppercase, lowercase, reverse, title"),
		mcp.WithString("input",
			mcp.Required(),
			mcp.Description("The input string to transform"),
		),
		mcp.WithString("operation",
			mcp.Required(),
			mcp.Description("The operation to perform"),
			mcp.Enum("uppercase", "lowercase", "reverse", "title"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Input     string `json:"input"`
			Operation string `json:"operation"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		var result string
		switch args.Operation {
		case "uppercase":
			result = strings.ToUpper(args.Input)
		case "lowercase":
			result = strings.ToLower(args.Input)
		case "reverse":
			runes := []rune(args.Input)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			result = string(runes)
		case "title":
			result = strings.Title(strings.ToLower(args.Input))
		default:
			return mcp.NewToolResultError(fmt.Sprintf("Unknown operation: %s", args.Operation)), nil
		}

		response := map[string]string{
			"input":     args.Input,
			"operation": args.Operation,
			"result":    result,
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 2: json_validate
// ============================================================================

func registerJSONValidateTool(s *server.MCPServer) {
	tool := mcp.NewTool("json_validate",
		mcp.WithDescription("Validates if a string is valid JSON"),
		mcp.WithString("json_string",
			mcp.Required(),
			mcp.Description("The JSON string to validate"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			JSONString string `json:"json_string"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		var jsonData interface{}
		err = json.Unmarshal([]byte(args.JSONString), &jsonData)

		response := map[string]interface{}{
			"valid": err == nil,
		}

		if err != nil {
			response["error"] = err.Error()
		} else {
			response["parsed"] = jsonData
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 3: uuid_generate
// ============================================================================

func registerUUIDGenerateTool(s *server.MCPServer) {
	tool := mcp.NewTool("uuid_generate",
		mcp.WithDescription("Generates a random UUID v4"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := uuid.New()

		response := map[string]string{
			"uuid": id.String(),
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 4: hash
// ============================================================================

func registerHashTool(s *server.MCPServer) {
	tool := mcp.NewTool("hash",
		mcp.WithDescription("Computes hash of input string using specified algorithm"),
		mcp.WithString("input",
			mcp.Required(),
			mcp.Description("The input string to hash"),
		),
		mcp.WithString("algorithm",
			mcp.Required(),
			mcp.Description("The hash algorithm to use"),
			mcp.Enum("md5", "sha256", "sha512"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Input     string `json:"input"`
			Algorithm string `json:"algorithm"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		var hashResult string
		switch args.Algorithm {
		case "md5":
			hash := md5.Sum([]byte(args.Input))
			hashResult = hex.EncodeToString(hash[:])
		case "sha256":
			hash := sha256.Sum256([]byte(args.Input))
			hashResult = hex.EncodeToString(hash[:])
		case "sha512":
			hash := sha512.Sum512([]byte(args.Input))
			hashResult = hex.EncodeToString(hash[:])
		default:
			return mcp.NewToolResultError(fmt.Sprintf("Unknown algorithm: %s", args.Algorithm)), nil
		}

		response := map[string]string{
			"input":     args.Input,
			"algorithm": args.Algorithm,
			"hash":      hashResult,
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 5: encode
// ============================================================================

func registerEncodeTool(s *server.MCPServer) {
	tool := mcp.NewTool("encode",
		mcp.WithDescription("Encodes input string using specified encoding"),
		mcp.WithString("input",
			mcp.Required(),
			mcp.Description("The input string to encode"),
		),
		mcp.WithString("encoding",
			mcp.Required(),
			mcp.Description("The encoding to use"),
			mcp.Enum("base64", "hex", "url"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Input    string `json:"input"`
			Encoding string `json:"encoding"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		var encoded string
		switch args.Encoding {
		case "base64":
			encoded = base64.StdEncoding.EncodeToString([]byte(args.Input))
		case "hex":
			encoded = hex.EncodeToString([]byte(args.Input))
		case "url":
			encoded = url.QueryEscape(args.Input)
		default:
			return mcp.NewToolResultError(fmt.Sprintf("Unknown encoding: %s", args.Encoding)), nil
		}

		response := map[string]string{
			"input":    args.Input,
			"encoding": args.Encoding,
			"encoded":  encoded,
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 6: decode
// ============================================================================

func registerDecodeTool(s *server.MCPServer) {
	tool := mcp.NewTool("decode",
		mcp.WithDescription("Decodes input string using specified encoding"),
		mcp.WithString("input",
			mcp.Required(),
			mcp.Description("The encoded input string to decode"),
		),
		mcp.WithString("encoding",
			mcp.Required(),
			mcp.Description("The encoding to use for decoding"),
			mcp.Enum("base64", "hex", "url"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Input    string `json:"input"`
			Encoding string `json:"encoding"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		var decoded string
		var decodeErr error

		switch args.Encoding {
		case "base64":
			decodedBytes, err := base64.StdEncoding.DecodeString(args.Input)
			if err != nil {
				decodeErr = err
			} else {
				decoded = string(decodedBytes)
			}
		case "hex":
			decodedBytes, err := hex.DecodeString(args.Input)
			if err != nil {
				decodeErr = err
			} else {
				decoded = string(decodedBytes)
			}
		case "url":
			var err error
			decoded, err = url.QueryUnescape(args.Input)
			if err != nil {
				decodeErr = err
			}
		default:
			return mcp.NewToolResultError(fmt.Sprintf("Unknown encoding: %s", args.Encoding)), nil
		}

		if decodeErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Decode error: %v", decodeErr)), nil
		}

		response := map[string]string{
			"input":    args.Input,
			"encoding": args.Encoding,
			"decoded":  decoded,
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}
