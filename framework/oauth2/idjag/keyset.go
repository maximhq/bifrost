package idjag

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Keys are reused for keyTTL; an unknown kid or a stale set refetches.
const (
	keyTTL      = time.Hour
	maxDocBytes = 1 << 20
)

// keySet caches one issuer's signing keys. The mutex guards only the cached
// state; each caller that misses fetches on its own context, outside the lock,
// so a slow IdP never blocks verifications the cached keys can answer and one
// caller's cancellation never fails another's fetch.
type keySet struct {
	issuer  string
	jwksURL string
	client  func() *http.Client

	mu        sync.RWMutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
}

// newKeySet returns an empty cache; jwksURL may be empty to use discovery.
func newKeySet(issuer, jwksURL string, client func() *http.Client) *keySet {
	return &keySet{issuer: issuer, jwksURL: jwksURL, client: client}
}

// key returns the verification key for kid, refreshing the set when it is stale
// or the kid is unknown. An empty kid matches only a single-key set.
func (k *keySet) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	if pub, ok, fresh := k.lookup(kid); ok && fresh {
		return pub, nil
	}
	keys, err := k.fetch(ctx)
	if err != nil {
		// A stale key still verifies while the IdP is unreachable.
		if pub, ok, _ := k.lookup(kid); ok {
			return pub, nil
		}
		return nil, fmt.Errorf("%w: kid %q: %v", ErrKey, kid, err)
	}
	k.mu.Lock()
	k.keys, k.fetchedAt = keys, time.Now()
	k.mu.Unlock()
	if pub, ok, _ := k.lookup(kid); ok {
		return pub, nil
	}
	return nil, fmt.Errorf("%w: kid %q", ErrKey, kid)
}

// lookup finds kid in the cached set and reports whether the set is fresh.
func (k *keySet) lookup(kid string) (pub crypto.PublicKey, ok, fresh bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	fresh = !k.fetchedAt.IsZero() && time.Since(k.fetchedAt) < keyTTL
	if kid == "" && len(k.keys) == 1 {
		for _, p := range k.keys {
			return p, true, fresh
		}
	}
	pub, ok = k.keys[kid]
	return pub, ok, fresh
}

// fetch downloads and parses the JWKS, discovering its URL first when needed.
func (k *keySet) fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	jwksURL := k.jwksURL
	if jwksURL == "" {
		discovered, err := k.discoverJWKSURL(ctx)
		if err != nil {
			return nil, err
		}
		jwksURL = discovered
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := k.getJSON(ctx, jwksURL, &doc); err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, j := range doc.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		if pub, err := j.publicKey(); err == nil {
			keys[j.Kid] = pub
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("jwks at %s holds no usable signing keys", jwksURL)
	}
	return keys, nil
}

// discoverJWKSURL reads jwks_uri from the issuer's OIDC or RFC 8414 metadata and
// requires the document to name the same issuer, so a redirecting proxy cannot
// substitute another IdP's keys.
func (k *keySet) discoverJWKSURL(ctx context.Context) (string, error) {
	base := strings.TrimRight(k.issuer, "/")
	var lastErr error
	for _, path := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server"} {
		var meta struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := k.getJSON(ctx, base+path, &meta); err != nil {
			lastErr = err
			continue
		}
		if !sameURL(meta.Issuer, k.issuer) || meta.JWKSURI == "" {
			lastErr = fmt.Errorf("metadata at %s names issuer %q", base+path, meta.Issuer)
			continue
		}
		return meta.JWKSURI, nil
	}
	return "", fmt.Errorf("discover jwks_uri for %s: %w", k.issuer, lastErr)
}

// getJSON GETs url and decodes a bounded JSON body into out. For an https
// issuer every hop stays on https: a discovery document, JWKS or redirect over
// plain http would let anyone on the network path substitute the signing keys.
func (k *keySet) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := k.client()
	if strings.HasPrefix(strings.ToLower(k.issuer), "https://") {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing %s: the issuer uses https", req.URL.Redacted())
		}
		httpsOnly := *client
		next := client.CheckRedirect
		httpsOnly.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			if r.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to %s: the issuer uses https", r.URL.Redacted())
			}
			if next != nil {
				return next(r, via)
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		}
		client = &httpsOnly
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocBytes))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// jwk is the subset of RFC 7517 fields needed for RSA and EC verification keys.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// publicKey converts the JWK into a crypto public key.
func (j jwk) publicKey() (crypto.PublicKey, error) {
	switch j.Kty {
	case "RSA":
		n, err := decodeBigInt(j.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeBigInt(j.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() > 1<<31-1 {
			return nil, fmt.Errorf("rsa exponent out of range")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("unsupported curve %q", j.Crv)
		}
		// RFC 7518 §6.2.1.2: x and y are exactly the curve's coordinate size, so
		// they are checked raw rather than through big.Int, which drops leading
		// zeros. ParseUncompressedPublicKey rejects points off the curve.
		size := (curve.Params().BitSize + 7) / 8
		x, errX := base64.RawURLEncoding.DecodeString(j.X)
		y, errY := base64.RawURLEncoding.DecodeString(j.Y)
		if errX != nil || errY != nil || len(x) != size || len(y) != size {
			return nil, fmt.Errorf("ec coordinates must be %d bytes each", size)
		}
		point := make([]byte, 0, 1+2*size)
		point = append(append(append(point, 4), x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
		if err != nil {
			return nil, fmt.Errorf("invalid ec public key: %w", err)
		}
		return pub, nil
	}
	return nil, fmt.Errorf("unsupported key type %q", j.Kty)
}

// decodeBigInt decodes an unpadded base64url big-endian integer.
func decodeBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("invalid jwk integer")
	}
	return new(big.Int).SetBytes(b), nil
}
