package main

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
)

// Simplified test structures
type TestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type TestBifrostParams struct {
	Model string `json:"model"`
}

type TestResponsesRequest struct {
	Input []TestMessage `json:"input"`
	TestBifrostParams
}

func (cr *TestBifrostParams) UnmarshalJSON(data []byte) error {
	// Use type alias to avoid infinite recursion
	type Alias TestBifrostParams
	aux := (*Alias)(cr)
	return sonic.Unmarshal(data, aux)
}

func main() {
	// Test JSON from the curl request
	jsonData := `{
		"model": "openai/gpt-5",
		"input": [
			{
				"role": "user",
				"content": "Show videos regarding Maxim AI on youtube"
			}
		]
	}`

	var req TestResponsesRequest

	// Test with sonic
	fmt.Println("Testing with sonic:")
	if err := sonic.Unmarshal([]byte(jsonData), &req); err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Success! Input length: %d\n", len(req.Input))
		fmt.Printf("Model: %s\n", req.Model)
		if len(req.Input) > 0 {
			fmt.Printf("First message: role=%s, content=%s\n", req.Input[0].Role, req.Input[0].Content)
		}
	}

	// Test with standard json
	fmt.Println("\nTesting with standard json:")
	var req2 TestResponsesRequest
	if err := json.Unmarshal([]byte(jsonData), &req2); err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Success! Input length: %d\n", len(req2.Input))
		fmt.Printf("Model: %s\n", req2.Model)
		if len(req2.Input) > 0 {
			fmt.Printf("First message: role=%s, content=%s\n", req2.Input[0].Role, req2.Input[0].Content)
		}
	}
}
