package gigachat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ParseGigaChatError parses GigaChat REST error responses.
func ParseGigaChatError(resp *fasthttp.Response, providerName schemas.ModelProvider) *schemas.BifrostError {
	var errorResp GigaChatErrorResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	if message := gigaChatErrorMessage(errorResp); message != "" {
		bifrostErr.Error.Message = message
	}
	if codeValue, ok := gigaChatErrorCode(errorResp); ok {
		code := codeValue
		bifrostErr.Error.Code = &code
	} else if errorResp.Status != nil {
		code := strconv.Itoa(*errorResp.Status)
		bifrostErr.Error.Code = &code
	}
	if errorResp.Status != nil && bifrostErr.StatusCode == nil {
		status := *errorResp.Status
		bifrostErr.StatusCode = &status
	}
	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("GigaChat API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "GigaChat API error"
		}
	}
	bifrostErr.Error.Message = redactGigaChatSensitiveText(bifrostErr.Error.Message)

	bifrostErr.ExtraFields.Provider = providerName
	return bifrostErr
}

func gigaChatErrorMessage(errorResp GigaChatErrorResponse) string {
	for _, message := range []string{errorResp.Message, errorResp.ErrorDescription, errorResp.Error} {
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func gigaChatErrorCode(errorResp GigaChatErrorResponse) (string, bool) {
	code := bytes.TrimSpace(errorResp.Code)
	if len(code) == 0 || bytes.Equal(code, []byte("null")) {
		return "", false
	}

	if len(code) >= 2 && code[0] == '"' {
		var value string
		if err := json.Unmarshal(code, &value); err != nil {
			return "", false
		}
		trimmed := strings.TrimSpace(value)
		return trimmed, trimmed != ""
	}

	trimmed := strings.TrimSpace(string(code))
	return trimmed, trimmed != ""
}
