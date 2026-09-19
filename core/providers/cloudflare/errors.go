package cloudflare

import (
	"fmt"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ParseCloudflareError parses Cloudflare's standard API error envelope.
func ParseCloudflareError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResponse CloudflareListModelsResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResponse)
	if bifrostErr == nil {
		return nil
	}
	return applyCloudflareError(bifrostErr, errorResponse.Errors)
}

// ToBifrostError converts a successful HTTP response carrying success=false into an error.
func (response *CloudflareListModelsResponse) ToBifrostError() *schemas.BifrostError {
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{},
	}
	if response == nil {
		bifrostErr.Error.Message = "Cloudflare model search failed"
		return bifrostErr
	}
	return applyCloudflareError(bifrostErr, response.Errors)
}

func applyCloudflareError(bifrostErr *schemas.BifrostError, errors []CloudflareAPIError) *schemas.BifrostError {
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if len(errors) > 0 {
		if strings.TrimSpace(errors[0].Message) != "" {
			bifrostErr.Error.Message = errors[0].Message
		}
		if errors[0].Code != 0 {
			bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(errors[0].Code))
		}
	}
	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("Cloudflare API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "Cloudflare model search failed"
		}
	}
	return bifrostErr
}
