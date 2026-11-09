package evals

import (
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// RedactedSecret stands in for a stored secret in API responses. Sent back unchanged
	// on an update, it means "keep the stored value".
	RedactedSecret = "<REDACTED>"
	// sealedPrefix marks a header value that is encrypted at rest.
	sealedPrefix = "enc:v1:"
)

// isSealed reports whether a stored header value is encrypted.
func isSealed(value string) bool { return strings.HasPrefix(value, sealedPrefix) }

// errSecretReference is returned for a header value that names a secret held
// elsewhere. The gateway resolves such a reference to one of its own secrets, and an
// API evaluator sends its headers to a URL the evaluator's author picks. Anyone who
// can edit an evaluator could then read the gateway's provider keys by pointing one at
// a server of their own, so a header value must be the value itself.
var errSecretReference = errors.New("a header value must be the value itself; a reference to an environment variable or a vault entry is not allowed here")

// isSecretReference reports whether a header value names a secret held elsewhere
// rather than holding one.
func isSecretReference(value string) bool {
	return strings.HasPrefix(value, "env.") || strings.HasPrefix(value, "vault.")
}

// NormalizeConfig validates a config and returns the form that is stored: defaults
// filled in, keys sorted, and for an API evaluator every header value encrypted.
func NormalizeConfig(evaluatorType EvaluatorType, raw []byte) ([]byte, error) {
	cfg, err := ParseConfig(evaluatorType, raw)
	if err != nil {
		return nil, err
	}
	normalized, err := MarshalConfig(cfg)
	if err != nil {
		return nil, err
	}
	if evaluatorType != EvaluatorTypeAPI {
		return normalized, nil
	}
	// The typed config holds resolved secrets, so the headers are rebuilt from the
	// request instead of being taken from it.
	headers := gjson.GetBytes(raw, "headers")
	if !headers.IsArray() {
		return sjson.DeleteBytes(normalized, "headers")
	}
	out := normalized
	if out, err = sjson.SetRawBytes(out, "headers", []byte("[]")); err != nil {
		return nil, err
	}
	for _, header := range headers.Array() {
		key := header.Get("key").String()
		stored, err := sealHeaderValue(header.Get("value"))
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", key, err)
		}
		entry, err := sjson.Set(`{}`, "key", key)
		if err != nil {
			return nil, err
		}
		if entry, err = sjson.Set(entry, "value", stored); err != nil {
			return nil, err
		}
		if out, err = sjson.SetRawBytes(out, "headers.-1", []byte(entry)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sealHeaderValue returns the stored form of one header value.
func sealHeaderValue(value gjson.Result) (string, error) {
	text := value.String()
	if value.IsObject() {
		// The SecretVar object form can carry a reference next to its resolved value.
		if value.Get("ref").String() != "" || value.Get("type").String() != "" {
			return "", errSecretReference
		}
		text = value.Get("value").String()
	}
	switch {
	case text == RedactedSecret:
		return "", errors.New("the redacted placeholder has no stored value to stand for")
	case isSecretReference(text):
		return "", errSecretReference
	case text == "" || isSealed(text) || !encrypt.IsEnabled():
		return text, nil
	}
	sealed, err := encrypt.Encrypt(text)
	if err != nil {
		return "", err
	}
	return sealedPrefix + sealed, nil
}

// unsealHeaderValue returns the value to send for a stored header value.
func unsealHeaderValue(stored string) (string, error) {
	if !isSealed(stored) {
		return stored, nil
	}
	return encrypt.Decrypt(strings.TrimPrefix(stored, sealedPrefix))
}

// RedactConfig returns a config that is safe to show: each API header value is replaced
// by a placeholder.
func RedactConfig(evaluatorType EvaluatorType, raw []byte) []byte {
	if evaluatorType != EvaluatorTypeAPI {
		return raw
	}
	out := raw
	for i, header := range gjson.GetBytes(raw, "headers").Array() {
		value := header.Get("value").String()
		if value == "" {
			continue
		}
		redacted, err := sjson.SetBytes(out, fmt.Sprintf("headers.%d.value", i), RedactedSecret)
		if err != nil {
			continue
		}
		out = redacted
	}
	return out
}

// endpointOf is the part of an API evaluator's URL that says where a request goes:
// the scheme and the host with its port, in lower case. Two URLs with the same
// endpoint reach the same server whatever their paths and queries are.
func endpointOf(rawURL string) string {
	rest := strings.TrimSpace(rawURL)
	if end := strings.IndexAny(rest, "?#"); end >= 0 {
		rest = rest[:end]
	}
	scheme, afterScheme, found := strings.Cut(rest, "://")
	if !found {
		return strings.ToLower(rest)
	}
	host, _, _ := strings.Cut(afterScheme, "/")
	// Anything before an "@" is user info, and the host is what follows it.
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	return strings.ToLower(scheme) + "://" + strings.ToLower(host)
}

// RestoreRedactedSecrets puts stored header values back where an incoming config
// carries the redacted placeholder, matching headers by name. This is what lets a
// client read a config, change something else and save it without retyping secrets.
//
// A stored value is put back only while the config still calls the server the value
// was entered for. If the scheme, host or port changed, the placeholder is refused:
// otherwise a caller could point the evaluator at a server of their own, keep the
// placeholder, and have the gateway send them the secret the API never shows.
func RestoreRedactedSecrets(evaluatorType EvaluatorType, incoming, stored []byte) ([]byte, error) {
	if evaluatorType != EvaluatorTypeAPI {
		return incoming, nil
	}
	sameEndpoint := endpointOf(gjson.GetBytes(incoming, "url").String()) == endpointOf(gjson.GetBytes(stored, "url").String())
	storedByKey := make(map[string]string)
	for _, header := range gjson.GetBytes(stored, "headers").Array() {
		key := strings.ToLower(header.Get("key").String())
		if _, seen := storedByKey[key]; !seen {
			storedByKey[key] = header.Get("value").String()
		}
	}
	out := incoming
	for i, header := range gjson.GetBytes(incoming, "headers").Array() {
		if header.Get("value").String() != RedactedSecret {
			continue
		}
		key := header.Get("key").String()
		value, ok := storedByKey[strings.ToLower(key)]
		if !ok {
			return nil, fmt.Errorf("header %q has no stored value to keep; enter its value", key)
		}
		if !sameEndpoint {
			return nil, fmt.Errorf("the URL now points at a different server, so the stored value of header %q is not sent there; enter its value again", key)
		}
		restored, err := sjson.SetBytes(out, fmt.Sprintf("headers.%d.value", i), value)
		if err != nil {
			return nil, err
		}
		out = restored
	}
	return out, nil
}
