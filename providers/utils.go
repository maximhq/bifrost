package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"

	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

// MergeConfig merges default config with custom parameters
func MergeConfig(defaultConfig map[string]interface{}, customParams map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})

	// Copy default config
	for k, v := range defaultConfig {
		merged[k] = v
	}

	// Override with custom parameters
	for k, v := range customParams {
		merged[k] = v
	}

	return merged
}

func PrepareParams(params *interfaces.ModelParameters) map[string]interface{} {
	flatParams := make(map[string]interface{})

	// Return empty map if params is nil
	if params == nil {
		return flatParams
	}

	// Use reflection to get the type and value of params
	val := reflect.ValueOf(params).Elem()
	typ := val.Type()

	// Iterate through all fields
	for i := range val.NumField() {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// Skip the ExtraParams field as it's handled separately
		if fieldType.Name == "ExtraParams" {
			continue
		}

		// Get the JSON tag name
		jsonTag := fieldType.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Strip out ,omitempty and others from the tag
		jsonTag = strings.Split(jsonTag, ",")[0]

		// Handle pointer fields
		if field.Kind() == reflect.Ptr && !field.IsNil() {
			flatParams[jsonTag] = field.Elem().Interface()
		}
	}

	// Handle ExtraParams
	maps.Copy(flatParams, params.ExtraParams)

	return flatParams
}

func SignAWSRequest(req *http.Request, accessKey, secretKey string, sessionToken *string, region, service string) *interfaces.BifrostError {
	// Set required headers before signing
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Calculate SHA256 hash of the request body
	var bodyHash string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return &interfaces.BifrostError{
				IsBifrostError: true,
				Error: interfaces.ErrorField{
					Message: "error reading request body",
					Error:   err,
				},
			}
		}
		// Restore the body for subsequent reads
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		hash := sha256.Sum256(bodyBytes)
		bodyHash = hex.EncodeToString(hash[:])
	} else {
		// For empty body, use the hash of an empty string
		hash := sha256.Sum256([]byte{})
		bodyHash = hex.EncodeToString(hash[:])
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			creds := aws.Credentials{
				AccessKeyID:     accessKey,
				SecretAccessKey: secretKey,
			}
			if sessionToken != nil {
				creds.SessionToken = *sessionToken
			}
			return creds, nil
		})),
	)
	if err != nil {
		return &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "failed to load aws config",
				Error:   err,
			},
		}
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(context.TODO())
	if err != nil {
		return &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "failed to retrieve aws credentials",
				Error:   err,
			},
		}
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(context.TODO(), creds, req, bodyHash, service, region, time.Now()); err != nil {
		return &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "failed to sign request",
				Error:   err,
			},
		}
	}

	return nil
}

// configureProxy sets up the proxy for the fasthttp client
func configureProxy(client *fasthttp.Client, proxyConfig *interfaces.ProxyConfig, logger interfaces.Logger) *fasthttp.Client {
	if proxyConfig == nil {
		return client
	}

	var dialFunc fasthttp.DialFunc

	// Create the appropriate proxy based on type
	switch proxyConfig.Type {
	case interfaces.NoProxy:
		return client
	case interfaces.HttpProxy:
		if proxyConfig.URL == "" {
			logger.Warn("Warning: HTTP proxy URL is required for setting up proxy")
			return client
		}
		dialFunc = fasthttpproxy.FasthttpHTTPDialer(proxyConfig.URL)
	case interfaces.Socks5Proxy:
		if proxyConfig.URL == "" {
			logger.Warn("Warning: SOCKS5 proxy URL is required for setting up proxy")
			return client
		}
		proxyUrl := proxyConfig.URL
		// Add authentication if provided
		if proxyConfig.Username != "" && proxyConfig.Password != "" {
			parsedURL, err := url.Parse(proxyConfig.URL)
			if err != nil {
				logger.Warn("Invalid proxy configuration: invalid SOCKS5 proxy URL")
				return client
			}
			// Set user and password in the parsed URL
			parsedURL.User = url.UserPassword(proxyConfig.Username, proxyConfig.Password)
			proxyUrl = parsedURL.String()
		}
		dialFunc = fasthttpproxy.FasthttpSocksDialer(proxyUrl)
	case interfaces.EnvProxy:
		// Use environment variables for proxy configuration
		dialFunc = fasthttpproxy.FasthttpProxyHTTPDialer()
	default:
		logger.Warn(fmt.Sprintf("Invalid proxy configuration: unsupported proxy type: %s", proxyConfig.Type))
		return client
	}

	if dialFunc != nil {
		client.Dial = dialFunc
	}

	return client
}
