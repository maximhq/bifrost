package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// writeTestPair writes a self-signed certificate for localhost with the given
// common name, so tests can tell which pair a handshake was served.
func writeTestPair(t *testing.T, dir, cn string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}

func servedCN(t *testing.T, r *certReloader) string {
	t.Helper()
	cert, err := r.getCertificate(nil)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	return leaf.Subject.CommonName
}

// forceReloadCheck makes the next handshake re-check the files and bumps their
// mod times, which a coarse-grained filesystem clock might otherwise leave equal.
func forceReloadCheck(t *testing.T, r *certReloader) {
	t.Helper()
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(r.certFile, future, future))
	require.NoError(t, os.Chtimes(r.keyFile, future, future))
	r.mu.Lock()
	r.lastCheck = time.Time{}
	r.mu.Unlock()
}

func TestCertReloaderFailsFastOnBadPair(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := writeTestPair(t, dir, "first")
	_, otherKey := writeTestPair(t, t.TempDir(), "other")

	_, err := newCertReloader(filepath.Join(dir, "missing.crt"), otherKey)
	assert.Error(t, err)
	_, err = newCertReloader(certFile, otherKey)
	assert.Error(t, err, "a key that does not match the certificate must be refused at startup")
}

func TestCertReloaderPicksUpRenewedPair(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	dir := t.TempDir()
	certFile, keyFile := writeTestPair(t, dir, "first")
	r, err := newCertReloader(certFile, keyFile)
	require.NoError(t, err)
	assert.Equal(t, "first", servedCN(t, r))

	writeTestPair(t, dir, "second")
	assert.Equal(t, "first", servedCN(t, r), "files are not re-checked before the interval")
	forceReloadCheck(t, r)
	assert.Equal(t, "second", servedCN(t, r))
}

func TestCertReloaderKeepsCurrentPairWhenRenewalIsBroken(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	dir := t.TempDir()
	certFile, keyFile := writeTestPair(t, dir, "first")
	r, err := newCertReloader(certFile, keyFile)
	require.NoError(t, err)

	// A renewal caught halfway: the certificate is new but the key is not.
	newCert, _ := writeTestPair(t, t.TempDir(), "second")
	data, err := os.ReadFile(newCert)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certFile, data, 0o644))
	forceReloadCheck(t, r)
	assert.Equal(t, "first", servedCN(t, r))

	// Once the matching key lands, the next check serves the new pair.
	writeTestPair(t, dir, "third")
	forceReloadCheck(t, r)
	assert.Equal(t, "third", servedCN(t, r))

	// A pair that disappears leaves the last good one in service.
	require.NoError(t, os.Remove(keyFile))
	r.mu.Lock()
	r.lastCheck = time.Time{}
	r.mu.Unlock()
	assert.Equal(t, "third", servedCN(t, r))
}

func TestListenRejectsIncompleteTLSSettings(t *testing.T) {
	certFile, keyFile := writeTestPair(t, t.TempDir(), "first")
	for name, s := range map[string]*BifrostHTTPServer{
		"cert without key":     {Host: "127.0.0.1", Port: "0", TLSCertFile: certFile},
		"key without cert":     {Host: "127.0.0.1", Port: "0", TLSKeyFile: keyFile},
		"tls port without tls": {Host: "127.0.0.1", Port: "0", TLSPort: "0"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.listen()
			assert.Error(t, err)
		})
	}
}

func TestListenServesPlainOrTLS(t *testing.T) {
	certFile, keyFile := writeTestPair(t, t.TempDir(), "first")
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(certFile)
	require.NoError(t, err)
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	get := func(t *testing.T, scheme, addr string) string {
		t.Helper()
		_, port, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		resp, err := client.Get(fmt.Sprintf("%s://localhost:%s/", scheme, port))
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return string(body)
	}

	cases := []struct {
		name    string
		server  *BifrostHTTPServer
		schemes []string
	}{
		{"plain only", &BifrostHTTPServer{Host: "127.0.0.1", Port: "0"}, []string{"http"}},
		{"tls on the main port", &BifrostHTTPServer{Host: "127.0.0.1", Port: "0", TLSCertFile: certFile, TLSKeyFile: keyFile}, []string{"https"}},
		{"plain plus a tls port", &BifrostHTTPServer{Host: "127.0.0.1", Port: "0", TLSPort: "0", TLSCertFile: certFile, TLSKeyFile: keyFile}, []string{"http", "https"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listeners, err := tc.server.listen()
			require.NoError(t, err)
			srv := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
				ctx.SetBodyString(fmt.Sprintf("tls=%v", ctx.IsTLS()))
			}}
			defer srv.Shutdown()
			require.Len(t, listeners, len(tc.schemes))
			for i, l := range listeners {
				assert.Equal(t, tc.schemes[i], l.scheme)
				go srv.Serve(l.ln)
				addr := l.ln.Addr().String()
				assert.Equal(t, fmt.Sprintf("tls=%v", l.scheme == "https"), get(t, l.scheme, addr))
			}
		})
	}
}

func TestCertReloaderPicksUpReplacementWithPreservedModTime(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	dir := t.TempDir()
	certFile, keyFile := writeTestPair(t, dir, "first")
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(certFile, stamp, stamp))
	require.NoError(t, os.Chtimes(keyFile, stamp, stamp))
	r, err := newCertReloader(certFile, keyFile)
	require.NoError(t, err)

	// A sync tool that copies with timestamps preserved: the new files land by
	// rename and carry the same mod time as the ones they replace.
	staged := t.TempDir()
	newCert, newKey := writeTestPair(t, staged, "second")
	require.NoError(t, os.Chtimes(newCert, stamp, stamp))
	require.NoError(t, os.Chtimes(newKey, stamp, stamp))
	require.NoError(t, os.Rename(newKey, keyFile))
	require.NoError(t, os.Rename(newCert, certFile))

	r.mu.Lock()
	r.lastCheck = time.Time{}
	r.mu.Unlock()
	assert.Equal(t, "second", servedCN(t, r))
}

func TestCertReloaderFollowsKubernetesSecretSymlinkSwap(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	// The layout kubelet gives a Secret volume: tls.crt -> ..data/tls.crt,
	// ..data -> ..<timestamp>/, and an update swaps ..data atomically.
	dir := t.TempDir()
	writeVersion := func(version, cn string) {
		vdir := filepath.Join(dir, version)
		require.NoError(t, os.Mkdir(vdir, 0o755))
		writeTestPair(t, vdir, cn)
	}
	writeVersion("..v1", "first")
	require.NoError(t, os.Symlink("..v1", filepath.Join(dir, "..data")))
	require.NoError(t, os.Symlink("..data/tls.crt", filepath.Join(dir, "tls.crt")))
	require.NoError(t, os.Symlink("..data/tls.key", filepath.Join(dir, "tls.key")))
	r, err := newCertReloader(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	require.NoError(t, err)
	assert.Equal(t, "first", servedCN(t, r))

	writeVersion("..v2", "second")
	require.NoError(t, os.Symlink("..v2", filepath.Join(dir, "..data_tmp")))
	require.NoError(t, os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")))
	r.mu.Lock()
	r.lastCheck = time.Time{}
	r.mu.Unlock()
	assert.Equal(t, "second", servedCN(t, r))
}
