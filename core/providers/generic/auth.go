package generic

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	case schemas.GenericAuthBearer, schemas.GenericAuthBasic:
		return "Authorization"
	}
	return ""
}

// stripCallerQueryCredential removes the auth query parameter a caller may have sent, so the
// configured key is the only credential upstream. Every other segment is forwarded byte for byte,
// in its original order and encoding: APIs that sign or compare the query string must see exactly
// what the caller sent. Only a segment's name is decoded, to recognise the parameter however it is
// spelled (api%5Fkey); a name that cannot be decoded, or a ";" separator whose meaning differs
// between servers, is refused rather than forwarded with the caller's credential possibly inside.
func (provider *GenericProvider) stripCallerQueryCredential(rawQuery string) (string, error) {
	param := provider.authQuery()
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
// A key with no value injects nothing (keyless providers).
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
	case schemas.GenericAuthQuery, schemas.GenericAuthNone:
		// query auth rides on the URL (appendAuthQuery); none injects nothing
	}
	return nil
}
