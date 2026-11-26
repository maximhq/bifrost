package relay

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
