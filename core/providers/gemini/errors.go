package gemini

import "github.com/maximhq/bifrost/core/schemas"

// ToGeminiError derives a GeminiChatRequestError from a BifrostError
func ToGeminiError(bifrostErr *schemas.BifrostError) *GeminiChatRequestError {
	if bifrostErr == nil {
		return nil
	}
	code := 500
	status := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Type != nil {		
		status = *bifrostErr.Error.Type
	}
	message := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		message = bifrostErr.Error.Message
	}
	if bifrostErr.StatusCode != nil {
		code = *bifrostErr.StatusCode
	}
	return &GeminiChatRequestError{
		Error: GeminiChatRequestErrorStruct{
			Code:    code,
			Message: message,
			Status:  status,
		},
	}
}
