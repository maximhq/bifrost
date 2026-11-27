package chaosmock

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// clientAssertionTypeJWTBearer marks a private_key_jwt client assertion (RFC 7523 §2.2).
const clientAssertionTypeJWTBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// assertionClaims are the claims a client assertion is checked against.
type assertionClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud any    `json:"aud"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"`
}

// assertionSubject returns the sub claim of an unverified assertion, which
// names the client to verify it against; "" when it cannot be read.
func assertionSubject(assertion string) string {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return ""
	}
	var claims assertionClaims
	if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
		_ = json.Unmarshal(raw, &claims)
	}
	return claims.Sub
}

// verifyClientAssertion checks an ES256 or RS256 client assertion against the
// client's registered public key: the signature, iss and sub naming the client,
// aud naming tokenURL, an exp in the future, and a jti.
func verifyClientAssertion(assertion string, client OAuthClient, tokenURL string) error {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return errors.New("client_assertion is not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(rawHeader, &header) != nil {
		return errors.New("client_assertion header is unreadable")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("client_assertion signature is unreadable")
	}
	block, _ := pem.Decode([]byte(client.PublicKeyPEM))
	if block == nil {
		return errors.New("the client's public key is not PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("the client's public key: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch key := pub.(type) {
	case *ecdsa.PublicKey:
		if header.Alg != "ES256" || len(sig) != 64 {
			return fmt.Errorf("client_assertion alg %q does not match an EC P-256 key", header.Alg)
		}
		if !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return errors.New("client_assertion signature does not verify")
		}
	case *rsa.PublicKey:
		if header.Alg != "RS256" {
			return fmt.Errorf("client_assertion alg %q does not match an RSA key", header.Alg)
		}
		if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
			return errors.New("client_assertion signature does not verify")
		}
	default:
		return errors.New("the client's public key is neither EC nor RSA")
	}
	var claims assertionClaims
	rawClaims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(rawClaims, &claims) != nil {
		return errors.New("client_assertion claims are unreadable")
	}
	audOK := claims.Aud == tokenURL
	if list, ok := claims.Aud.([]any); ok {
		for _, a := range list {
			audOK = audOK || a == tokenURL
		}
	}
	switch {
	case claims.Iss != client.ClientID || claims.Sub != client.ClientID:
		return errors.New("client_assertion iss and sub must be the client_id")
	case !audOK:
		return fmt.Errorf("client_assertion aud must be %s", tokenURL)
	case claims.Exp <= time.Now().Unix():
		return errors.New("client_assertion has expired")
	case claims.Jti == "":
		return errors.New("client_assertion has no jti")
	}
	return nil
}
