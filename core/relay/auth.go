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

// hasPrefixFold reports whether s begins with prefix, matched ASCII case-insensitively without allocating.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// unescapeQueryPart query-unescapes s only when percent-encoding or '+' is present, returning s unchanged otherwise.
func unescapeQueryPart(s string) (string, error) {
	if !strings.ContainsAny(s, "%+") {
		return s, nil
	}
	return url.QueryUnescape(s)
}

// extractCallerCredential reads the credential a caller presented in the endpoint's configured
// auth slot (bearer token, custom header with an optional value prefix, query parameter, or HTTP
// Basic auth), or "" when none was presented. Callers pointing an existing SDK at a relay send
// their Bifrost virtual key in the slot the upstream API expects; the transport promotes a
// virtual-key-shaped value to the caller's identity before stripping it.
func (e *endpoint) extractCallerCredential(peekHeader func(string) string, rawQuery string) string {
	switch e.cfg.Auth.Type {
	case schemas.RelayAuthBearer:
		if peekHeader == nil {
			return ""
		}
		val := strings.TrimSpace(peekHeader("Authorization"))
		if !hasPrefixFold(val, "bearer ") {
			return ""
		}
		return strings.TrimSpace(val[len("bearer "):])
	case schemas.RelayAuthHeader:
		if e.cfg.Auth.HeaderName == "" || peekHeader == nil {
			return ""
		}
		val := strings.TrimSpace(peekHeader(e.cfg.Auth.HeaderName))
		if val == "" {
			return ""
		}
		if prefix := e.cfg.Auth.ValuePrefix; prefix != "" {
			if !hasPrefixFold(val, prefix) {
				return ""
			}
			val = strings.TrimSpace(val[len(prefix):])
		}
		return val
	case schemas.RelayAuthQuery:
		param := e.authQuery()
		if param == "" || rawQuery == "" || strings.Contains(rawQuery, ";") ||
			(!strings.Contains(rawQuery, param) && !strings.Contains(rawQuery, "%")) {
			return ""
		}
		for rest := rawQuery; rest != ""; {
			var segment string
			segment, rest, _ = strings.Cut(rest, "&")
			name, val, hasVal := strings.Cut(segment, "=")
			if !hasVal {
				continue
			}
			decodedName, err := unescapeQueryPart(name)
			if err != nil || decodedName != param {
				continue
			}
			decodedVal, err := unescapeQueryPart(val)
			if err != nil {
				return ""
			}
			if trimmed := strings.TrimSpace(decodedVal); trimmed != "" {
				return trimmed
			}
		}
		return ""
	case schemas.RelayAuthBasic:
		if peekHeader == nil {
			return ""
		}
		auth := strings.TrimSpace(peekHeader("Authorization"))
		if !hasPrefixFold(auth, "basic ") {
			return ""
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[len("basic "):]))
		if err != nil {
			return ""
		}
		decoded := string(raw)
		user, pass, hasColon := strings.Cut(decoded, ":")
		if !hasColon {
			return strings.TrimSpace(decoded)
		}
		u := strings.TrimSpace(user)
		if hasPrefixFold(u, "sk-bf-") {
			return u
		}
		p := strings.TrimSpace(pass)
		if hasPrefixFold(p, "sk-bf-") {
			return p
		}
		return ""
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
