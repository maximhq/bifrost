package sagemaker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
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
	creds, bErr := bedrock.ResolveAWSCredentials(ctx, bedrockKeyConfig(key.SageMakerKeyConfig), region)
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
	// Sign a net/http request, then copy its headers onto the fasthttp one so the proxy and TLS settings apply.
	signReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to build sagemaker ListEndpoints request", err)
	}
	signReq.Header.Set("Content-Type", "application/x-amz-json-1.1")
	signReq.Header.Set("X-Amz-Target", "SageMaker.ListEndpoints")
	hash := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(ctx, creds, signReq, hex.EncodeToString(hash[:]), "sagemaker", region, time.Now()); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to sign sagemaker ListEndpoints request", err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	for name, values := range signReq.Header {
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
