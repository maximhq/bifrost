package generic

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

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
	if param == "" || rawQuery == "" || !strings.Contains(rawQuery, param) {
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

// signAWSRequest is wired in the SigV4 change; until then the auth type is refused at request time.
func (provider *GenericProvider) signAWSRequest(_ *schemas.BifrostContext, _ *http.Request, _ schemas.Key) *schemas.BifrostError {
	return providerUtils.NewBifrostBadRequestError("aws_sigv4 auth is not supported by the generic provider yet")
}
