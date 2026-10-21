package generic

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// authQuery returns the query parameter the selected key is sent in, or "" for other auth types.
func (provider *GenericProvider) authQuery() string {
	if provider.cfg.Auth.Type == schemas.GenericAuthQuery {
		return provider.cfg.Auth.QueryParam
	}
	return ""
}

// authHeader returns the header the selected key is sent in, or "" for other auth types.
func (provider *GenericProvider) authHeader() string {
	switch provider.cfg.Auth.Type {
	case schemas.GenericAuthHeader:
		return provider.cfg.Auth.HeaderName
	case schemas.GenericAuthBearer, schemas.GenericAuthBasic, schemas.GenericAuthAWSSigV4:
		return "Authorization"
	}
	return ""
}

// stripCallerQueryCredential removes the auth query parameter a caller may have sent, so the
// configured key is the only credential upstream. It is a no-op when the parameter is absent.
func (provider *GenericProvider) stripCallerQueryCredential(rawQuery string) string {
	param := provider.authQuery()
	// Cheap gate: the parameter name appears literally, or something is percent-encoded and may
	// spell it. Everything else is left untouched without a parse.
	if param == "" || rawQuery == "" || (!strings.Contains(rawQuery, param) && !strings.Contains(rawQuery, "%")) {
		return rawQuery
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	if _, present := values[param]; !present {
		return rawQuery
	}
	delete(values, param)
	return values.Encode()
}

// appendAuthQuery adds the key as a query parameter for the query auth type.
func (provider *GenericProvider) appendAuthQuery(rawQuery string, key schemas.Key) string {
	param := provider.authQuery()
	value := key.Value.GetValue()
	if param == "" || value == "" {
		return rawQuery
	}
	encoded := url.QueryEscape(param) + "=" + url.QueryEscape(value)
	if rawQuery == "" {
		return encoded
	}
	return rawQuery + "&" + encoded
}

// applyAuth injects the selected key into the upstream request per the configured auth type.
// A key with no value injects nothing (keyless providers, or SigV4 keys that carry AWS credentials).
func (provider *GenericProvider) applyAuth(ctx *schemas.BifrostContext, httpReq *http.Request, key schemas.Key) *schemas.BifrostError {
	value := key.Value.GetValue()
	switch provider.cfg.Auth.Type {
	case schemas.GenericAuthBearer:
		if value != "" {
			httpReq.Header.Set("Authorization", "Bearer "+value)
		}
	case schemas.GenericAuthHeader:
		if value != "" {
			httpReq.Header.Set(provider.cfg.Auth.HeaderName, provider.cfg.Auth.ValuePrefix+value)
		}
	case schemas.GenericAuthBasic:
		if value != "" {
			httpReq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(value)))
		}
	case schemas.GenericAuthAWSSigV4:
		return provider.signAWSRequest(ctx, httpReq, key)
	case schemas.GenericAuthQuery, schemas.GenericAuthNone:
		// query auth rides on the URL (appendAuthQuery); none injects nothing
	}
	return nil
}

// sigv4CallerHeaders are the signature-bearing headers a caller may send that must never be
// signed or forwarded: the signer owns them. Other x-amz-* headers (X-Amz-Target names the
// Textract or Translate operation) are the request itself and are kept.
var sigv4CallerHeaders = []string{"X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "X-Amz-Credential", "X-Amz-Signature", "X-Amz-Algorithm", "X-Amz-Signedheaders", "X-Amz-Expires"}

// signAWSRequest SigV4-signs the request with the key's bedrock_key_config credentials for the
// configured service. The region comes from auth.aws.region, else the key's region.
func (provider *GenericProvider) signAWSRequest(ctx *schemas.BifrostContext, httpReq *http.Request, key schemas.Key) *schemas.BifrostError {
	region := ""
	if provider.cfg.Auth.AWS != nil {
		region = provider.cfg.Auth.AWS.Region
	}
	if region == "" && key.BedrockKeyConfig != nil && key.BedrockKeyConfig.Region != nil {
		region = key.BedrockKeyConfig.Region.GetValue()
	}
	if region == "" {
		return providerUtils.NewBifrostBadRequestError("aws_sigv4 auth needs a region: set generic_config.auth.aws.region or the key's bedrock_key_config.region")
	}
	for _, name := range sigv4CallerHeaders {
		httpReq.Header.Del(name)
	}
	return bedrock.SignAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, provider.cfg.Auth.AWS.Service)
}
