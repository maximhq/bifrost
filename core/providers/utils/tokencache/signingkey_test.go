package tokencache

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecKeyOn(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	require.NoError(t, err)
	return key
}

func TestParsePEMPrivateKey(t *testing.T) {
	rsaKey, rsaPKCS1 := rsaPEM(t)
	_, ecSEC1 := ecPEM(t)
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	require.NoError(t, err)
	rsaPKCS8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	edDER, err := x509.MarshalPKCS8PrivateKey(edKey)
	require.NoError(t, err)
	ed25519PEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER}))

	for name, tc := range map[string]struct {
		pem string
		ok  bool
	}{
		"rsa pkcs1":                     {rsaPKCS1, true},
		"rsa pkcs8":                     {rsaPKCS8, true},
		"ec sec1":                       {ecSEC1, true},
		"escaped newlines are repaired": {strings.ReplaceAll(strings.TrimSpace(ecSEC1), "\n", `\n`), true},
		"ed25519 is refused":            {ed25519PEM, false},
		"not a pem":                     {"not a key", false},
		"trailing data is refused":      {ecSEC1 + "junk", false},
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := ParsePEMPrivateKey(tc.pem)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestKeyMatchesSigningAlgorithm(t *testing.T) {
	rsaKey, _ := rsaPEM(t)
	p256 := ecKeyOn(t, elliptic.P256())

	require.NoError(t, KeyMatchesSigningAlgorithm(rsaKey, "", "f"), "empty means RS256")
	require.NoError(t, KeyMatchesSigningAlgorithm(rsaKey, "PS384", "f"))
	require.NoError(t, KeyMatchesSigningAlgorithm(p256, "ES256", "f"))
	assert.EqualError(t, KeyMatchesSigningAlgorithm(p256, "", "f"), "f RS256 needs an RSA private key; private_key is EC (set an ES* algorithm)")
	assert.EqualError(t, KeyMatchesSigningAlgorithm(rsaKey, "ES256", "f"), "f ES256 needs an EC private key; private_key is RSA")
	assert.EqualError(t, KeyMatchesSigningAlgorithm(p256, "ES384", "f"), "f ES384 needs a P-384 key; private_key is on P-256")
}

// TestDefaultSigningAlgorithm: an EC key's curve fixes its algorithm; an RSA key
// takes the first RSA algorithm the authorization server advertises, else RS256.
func TestDefaultSigningAlgorithm(t *testing.T) {
	rsaKey, _ := rsaPEM(t)
	for name, tc := range map[string]struct {
		key        any
		advertised []string
		want       string
	}{
		"P-256":                        {ecKeyOn(t, elliptic.P256()), []string{"RS256"}, "ES256"},
		"P-384":                        {ecKeyOn(t, elliptic.P384()), nil, "ES384"},
		"P-521":                        {ecKeyOn(t, elliptic.P521()), nil, "ES512"},
		"RSA, nothing advertised":      {rsaKey, nil, "RS256"},
		"RSA, PS only advertised":      {rsaKey, []string{"ES256", "PS256"}, "PS256"},
		"RSA, first RSA one wins":      {rsaKey, []string{"RS512", "RS256"}, "RS512"},
		"RSA, no RSA one advertised":   {rsaKey, []string{"ES256"}, "RS256"},
		"RSA, unsupported one skipped": {rsaKey, []string{"RS1", "PS512"}, "PS512"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DefaultSigningAlgorithm(tc.key, tc.advertised)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := DefaultSigningAlgorithm(ecKeyOn(t, elliptic.P224()), nil)
	assert.Error(t, err, "P-224 has no supported algorithm")
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = DefaultSigningAlgorithm(edKey, nil)
	assert.Error(t, err)
}
