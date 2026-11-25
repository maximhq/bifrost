package tokencache

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
)

// ecAlgorithmCurves maps each ES algorithm to the one curve RFC 7518 §3.4 defines it for.
var ecAlgorithmCurves = map[string]string{"ES256": "P-256", "ES384": "P-384", "ES512": "P-521"}

// ParsePEMPrivateKey parses s as an RSA or EC private key Bifrost can sign a JWT
// assertion with: PKCS#1, SEC 1, or a PKCS#8 container holding either. Literal
// backslash-n is repaired first, because that is how a PEM most often arrives from a
// JSON config or an environment variable.
func ParsePEMPrivateKey(s string) (crypto.PrivateKey, bool) {
	block, rest := pem.Decode([]byte(NormalizePEM(s)))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, false
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		return key, err == nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		return key, err == nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, false
		}
		switch key.(type) {
		case *rsa.PrivateKey, *ecdsa.PrivateKey:
			return key, true
		}
		return nil, false
	default:
		return nil, false
	}
}

// KeyMatchesSigningAlgorithm checks a parsed key against the algorithm it will sign
// with (RS256 when unset): RSA for RS*/PS*, and for ES* an EC key on the curve the
// algorithm names. field names the algorithm setting in errors. The signer selects its
// key parser from the algorithm, so a mismatch would otherwise save cleanly and then
// fail at the first token request, far from the field that caused it.
func KeyMatchesSigningAlgorithm(key crypto.PrivateKey, alg, field string) error {
	if alg == "" {
		alg = "RS256"
	}
	if strings.HasPrefix(alg, "ES") {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return fmt.Errorf("%s %s needs an EC private key; private_key is RSA", field, alg)
		}
		want := ecAlgorithmCurves[alg]
		if got := ec.Curve.Params().Name; want != "" && got != want {
			return fmt.Errorf("%s %s needs a %s key; private_key is on %s", field, alg, want, got)
		}
		return nil
	}
	if _, ok := key.(*rsa.PrivateKey); !ok {
		return fmt.Errorf("%s %s needs an RSA private key; private_key is EC (set an ES* algorithm)", field, alg)
	}
	return nil
}

// DefaultSigningAlgorithm picks the algorithm to sign with key when none is set. An EC
// key gets the one ES algorithm defined for its curve. An RSA key gets the first RS* or
// PS* algorithm in advertised (an authorization server's
// token_endpoint_auth_signing_alg_values_supported), else RS256.
func DefaultSigningAlgorithm(key crypto.PrivateKey, advertised []string) (string, error) {
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		curve := k.Curve.Params().Name
		for alg, c := range ecAlgorithmCurves {
			if c == curve {
				return alg, nil
			}
		}
		return "", fmt.Errorf("private_key is on curve %s, which no supported algorithm signs with", curve)
	case *rsa.PrivateKey:
		for _, alg := range advertised {
			if (strings.HasPrefix(alg, "RS") || strings.HasPrefix(alg, "PS")) && slices.Contains(rsaSigningAlgorithms, alg) {
				return alg, nil
			}
		}
		return "RS256", nil
	}
	return "", fmt.Errorf("private_key must be an RSA or EC key")
}

// rsaSigningAlgorithms lists the supported algorithms an RSA key signs with.
var rsaSigningAlgorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"}
