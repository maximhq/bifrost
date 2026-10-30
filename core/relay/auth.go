package relay

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// authQuery returns the query parameter the credential is sent in, or "" for other auth types.
func (e *endpoint) authQuery() string {
	if e.cfg.Auth.Type == schemas.RelayAuthQuery {
		return e.cfg.Auth.QueryParam
	}
	return ""
}

// authHeader returns the header the credential is sent in, or "" for other auth types.
func (e *endpoint) authHeader() string {
	switch e.cfg.Auth.Type {
	case schemas.RelayAuthHeader:
		return e.cfg.Auth.HeaderName
	case schemas.RelayAuthBearer, schemas.RelayAuthBasic, schemas.RelayAuthAWSSigV4:
		return "Authorization"
	}
	return ""
}

// stripCallerQueryCredential removes the auth query parameter a caller may have sent, so the
// configured credential is the only one upstream. Every other segment is forwarded byte for byte,
// in its original order and encoding: APIs that sign or compare the query string must see exactly
// what the caller sent. Only a segment's name is decoded, to recognise the parameter however it is
// spelled (api%5Fkey); a name that cannot be decoded, or a ";" separator whose meaning differs
// between servers, is refused rather than forwarded with the caller's credential possibly inside.
func (e *endpoint) stripCallerQueryCredential(rawQuery string) (string, error) {
	param := e.authQuery()
	// Cheap gate: the parameter name appears literally, or something is percent-encoded and may
	// spell it. Everything else is left untouched without a scan.
	if param == "" || rawQuery == "" || (!strings.Contains(rawQuery, param) && !strings.Contains(rawQuery, "%")) {
		return rawQuery, nil
	}
	if strings.Contains(rawQuery, ";") {
		return "", fmt.Errorf("query string cannot be parsed: a ; separator is ambiguous under query auth")
	}
	var kept []string
	dropped := false
	for _, segment := range strings.Split(rawQuery, "&") {
		name, _, _ := strings.Cut(segment, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			return "", fmt.Errorf("query string cannot be parsed: %w", err)
		}
		if decoded == param {
			dropped = true
			continue
		}
		kept = append(kept, segment)
	}
	if !dropped {
		return rawQuery, nil
	}
	return strings.Join(kept, "&"), nil
}

// appendAuthQuery adds the credential as a query parameter for the query auth type.
func (e *endpoint) appendAuthQuery(rawQuery string) string {
	param := e.authQuery()
	value := e.secret()
	if param == "" || value == "" {
		return rawQuery
	}
	encoded := url.QueryEscape(param) + "=" + url.QueryEscape(value)
	if rawQuery == "" {
		return encoded
	}
	return rawQuery + "&" + encoded
}

// secret is the credential's value for the token auth types, "" when none is set.
func (e *endpoint) secret() string {
	if e.cfg.Credential == nil || e.cfg.Credential.Value == nil {
		return ""
	}
	return e.cfg.Credential.Value.GetValue()
}

// applyAuth injects the credential into the upstream request per the configured auth type.
func (e *endpoint) applyAuth(ctx *schemas.BifrostContext, httpReq *http.Request) *schemas.BifrostError {
	value := e.secret()
	switch e.cfg.Auth.Type {
	case schemas.RelayAuthBearer:
		if value != "" {
			httpReq.Header.Set("Authorization", "Bearer "+value)
		}
	case schemas.RelayAuthHeader:
		if value != "" {
			httpReq.Header.Set(e.cfg.Auth.HeaderName, e.cfg.Auth.ValuePrefix+value)
		}
	case schemas.RelayAuthBasic:
		if value != "" {
			httpReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(value)))
		}
	case schemas.RelayAuthAWSSigV4:
		return e.signAWSRequest(ctx, httpReq)
	case schemas.RelayAuthQuery, schemas.RelayAuthNone:
		// query auth rides on the URL (appendAuthQuery); none injects nothing
	}
	return nil
}

// sigv4CallerHeaders are the signature-bearing headers a caller may send that must never be
// signed or forwarded: the signer owns them. Other x-amz-* headers (X-Amz-Target names the
// Textract or Translate operation) are the request itself and are kept.
var sigv4CallerHeaders = []string{"X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "X-Amz-Credential", "X-Amz-Signature", "X-Amz-Algorithm", "X-Amz-Signedheaders", "X-Amz-Expires"}

// signAWSRequest SigV4-signs the request with the endpoint's AWS credential for the configured
// service and the credential's region, through the Bedrock signer so credential handling (static,
// assumed role, default chain) lives in one place.
func (e *endpoint) signAWSRequest(ctx *schemas.BifrostContext, httpReq *http.Request) *schemas.BifrostError {
	if e.awsKey == nil || e.awsRegion == "" {
		return providerUtils.NewBifrostBadRequestError("aws_sigv4 auth needs credential.aws.region")
	}
	for _, name := range sigv4CallerHeaders {
		httpReq.Header.Del(name)
	}
	return bedrock.SignAWSRequest(ctx, httpReq, e.awsKey, e.awsRegion, e.cfg.Auth.AWS.Service)
}

// presignedURLExpiry is how long a presigned WebSocket handshake URL is valid. The handshake is
// sent immediately, so this only has to cover clock skew and the dial.
const presignedURLExpiry = 5 * time.Minute

// emptyPayloadSHA256 is the SHA-256 of an empty body, the payload hash a handshake GET is signed with.
const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// presignAWSRequest SigV4-presigns a WebSocket handshake: the signature rides in the query, as AWS
// streaming APIs over WebSocket (Transcribe) require, and only the host is signed, so the headers
// the dialer adds to the upgrade never invalidate it. Signature parameters a caller sent in the
// query are dropped first: the signer owns them. Credentials resolve as for header signing.
func (e *endpoint) presignAWSRequest(ctx *schemas.BifrostContext, httpReq *http.Request) *schemas.BifrostError {
	if e.awsKey == nil || e.awsRegion == "" {
		return providerUtils.NewBifrostBadRequestError("aws_sigv4 auth needs credential.aws.region")
	}
	creds, bifrostErr := bedrock.ResolveAWSCredentials(ctx, e.awsKey, e.awsRegion)
	if bifrostErr != nil {
		return bifrostErr
	}
	query := httpReq.URL.Query()
	for name := range query {
		for _, owned := range sigv4CallerHeaders {
			if strings.EqualFold(name, owned) {
				query.Del(name)
			}
		}
	}
	query.Set("X-Amz-Expires", fmt.Sprintf("%d", int(presignedURLExpiry/time.Second)))
	httpReq.URL.RawQuery = query.Encode()
	toSign, err := http.NewRequestWithContext(ctx, http.MethodGet, httpReq.URL.String(), nil)
	if err != nil {
		return providerUtils.NewBifrostOperationError("error creating presign request", err)
	}
	signed, _, err := v4.NewSigner().PresignHTTP(ctx, creds, toSign, emptyPayloadSHA256, e.cfg.Auth.AWS.Service, e.awsRegion, time.Now().UTC())
	if err != nil {
		return providerUtils.NewBifrostOperationError("error presigning the websocket handshake", err)
	}
	signedURL, err := url.Parse(signed)
	if err != nil {
		return providerUtils.NewBifrostOperationError("error presigning the websocket handshake", err)
	}
	httpReq.URL = signedURL
	for _, name := range sigv4CallerHeaders {
		httpReq.Header.Del(name)
	}
	return nil
}

// bedrockKeyConfig maps an endpoint AWS credential onto the Bedrock key config the signer reads.
// Built once per endpoint (re)build.
func bedrockKeyConfig(c *schemas.RelayAWSCredential) *schemas.BedrockKeyConfig {
	if c == nil {
		return nil
	}
	cfg := &schemas.BedrockKeyConfig{
		SessionToken:    c.SessionToken,
		Region:          c.Region,
		RoleARN:         c.RoleARN,
		ExternalID:      c.ExternalID,
		RoleSessionName: c.SessionName,
	}
	if c.AccessKey != nil {
		cfg.AccessKey = *c.AccessKey
	}
	if c.SecretKey != nil {
		cfg.SecretKey = *c.SecretKey
	}
	return cfg
}
