package sagemaker

import (
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	listEndpointsPageSize = 100 // ListEndpoints' MaxResults ceiling
	listEndpointsMaxPages = 50  // stops discovery at 5000 endpoints per key
)

// controlPlaneURL is the regional SageMaker API URL that serves ListEndpoints.
func controlPlaneURL(region string) string {
	suffix := "amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return "https://api.sagemaker." + region + "." + suffix + "/"
}

// listEndpoints returns the names of every InService endpoint the key's credentials can see in its region.
func (provider *SageMakerProvider) listEndpoints(ctx *schemas.BifrostContext, key schemas.Key, region string) ([]string, *schemas.BifrostError) {
	creds, bErr := provider.awsCredentials(ctx, key, region)
	if bErr != nil {
		return nil, bErr
	}
	var names []string
	nextToken := ""
	for page := 0; page < listEndpointsMaxPages; page++ {
		parsed, bErr := provider.listEndpointsPage(ctx, creds, region, nextToken)
		if bErr != nil {
			return nil, bErr
		}
		for _, ep := range parsed.Endpoints {
			names = append(names, ep.EndpointName)
		}
		if parsed.NextToken == "" {
			return names, nil
		}
		nextToken = parsed.NextToken
	}
	provider.logger.Warn("sagemaker: stopped endpoint discovery after %d endpoints for key %s", len(names), key.Name)
	return names, nil
}

// listEndpointsPage sends one SigV4-signed ListEndpoints call.
func (provider *SageMakerProvider) listEndpointsPage(ctx *schemas.BifrostContext, creds aws.Credentials, region, nextToken string) (*sageMakerListEndpointsResponse, *schemas.BifrostError) {
	body, err := sonic.Marshal(sageMakerListEndpointsRequest{MaxResults: listEndpointsPageSize, NextToken: nextToken, StatusEquals: "InService"})
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to marshal sagemaker ListEndpoints request", err)
	}
	url := controlPlaneURL(region)
	signed, bErr := signRequest(ctx, creds, region, http.MethodPost, url, body, map[string]string{
		"Content-Type": "application/x-amz-json-1.1",
		"X-Amz-Target": "SageMaker.ListEndpoints",
	})
	if bErr != nil {
		return nil, bErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	for name, values := range signed {
		req.Header.Set(name, values[0])
	}
	req.SetBody(body)
	_, bErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bErr != nil {
		return nil, bErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.NewProviderAPIError("sagemaker ListEndpoints failed: "+string(resp.Body()), nil, resp.StatusCode(), nil, nil)
	}
	var parsed sageMakerListEndpointsResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to parse sagemaker ListEndpoints response", err)
	}
	return &parsed, nil
}
