package scenarios

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Tool definitions for testing
var WeatherToolDefinition = schemas.Tool{
	Type: "function",
	Function: schemas.Function{
		Name:        "get_weather",
		Description: "Get the current weather in a given location",
		Parameters: schemas.FunctionParameters{
			Type: "object",
			Properties: map[string]interface{}{
				"location": map[string]interface{}{
					"type":        "string",
					"description": "The city and state, e.g. San Francisco, CA",
				},
				"unit": map[string]interface{}{
					"type": "string",
					"enum": []string{"celsius", "fahrenheit"},
				},
			},
			Required: []string{"location"},
		},
	},
}

var CalculatorToolDefinition = schemas.Tool{
	Type: "function",
	Function: schemas.Function{
		Name:        "calculate",
		Description: "Perform basic mathematical calculations",
		Parameters: schemas.FunctionParameters{
			Type: "object",
			Properties: map[string]interface{}{
				"expression": map[string]interface{}{
					"type":        "string",
					"description": "The mathematical expression to evaluate, e.g. '2 + 3' or '10 * 5'",
				},
			},
			Required: []string{"expression"},
		},
	},
}

var TimeToolDefinition = schemas.Tool{
	Type: "function",
	Function: schemas.Function{
		Name:        "get_current_time",
		Description: "Get the current time in a specific timezone",
		Parameters: schemas.FunctionParameters{
			Type: "object",
			Properties: map[string]interface{}{
				"timezone": map[string]interface{}{
					"type":        "string",
					"description": "The timezone identifier, e.g. 'America/New_York' or 'UTC'",
				},
			},
			Required: []string{"timezone"},
		},
	},
}

// Test images for testing
const TestImageURL = "https://upload.wikimedia.org/wikipedia/commons/a/a7/Camponotus_flavomarginatus_ant.jpg"
const TestImageBase64 = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k="

// Helper functions for creating requests
func CreateBasicChatMessage(content string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateImageMessage(text, imageURL string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
			ContentBlocks: &[]schemas.ContentBlock{
				{
					Type: schemas.ContentBlockTypeText,
					Text: bifrost.Ptr(text),
				},
				{
					Type: schemas.ContentBlockTypeImage,
					ImageURL: &schemas.ImageURLStruct{
						URL: imageURL,
					},
				},
			},
		},
	}
}

func CreateToolMessage(content string, toolCallID string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleTool,
		Content: schemas.MessageContent{
			ContentStr: bifrost.Ptr(content),
		},
		ToolMessage: &schemas.ToolMessage{
			ToolCallID: &toolCallID,
		},
	}
}

func GetResultContent(result *schemas.BifrostResponse) string {
	if result == nil || len(result.Choices) == 0 {
		return ""
	}

	resultContent := ""
	if result.Choices[0].Message.Content.ContentStr != nil {
		resultContent = *result.Choices[0].Message.Content.ContentStr
	} else if result.Choices[0].Message.Content.ContentBlocks != nil {
		for _, block := range *result.Choices[0].Message.Content.ContentBlocks {
			if block.Text != nil {
				resultContent += *block.Text
			}
		}
	}
	return resultContent
}
