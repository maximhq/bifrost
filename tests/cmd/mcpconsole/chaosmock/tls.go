package chaosmock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"net"
	"net/http"
	"time"
)

// ServeTLS serves the fleet over HTTPS with a temporary loopback certificate.
func (f *Fleet) ServeTLS(addr string) (string, func(context.Context) error, error) {
	cert, err := selfSignedCert()
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		Handler:   f.Handler(),
		ConnState: f.conns.hook(),
	}
	go func() {
		// A closed listener is the normal way this ends, so it is not news.
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("chaosmock: tls serve stopped: %v", err)
		}
	}()
	tlsURL := "https://" + ln.Addr().String()
	f.mu.Lock()
	f.tlsURL = tlsURL
	f.tlsCertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	f.mu.Unlock()
	return tlsURL, srv.Shutdown, nil
}

// tlsListener returns the URL and PEM certificate of the listener ServeTLS
// opened, both empty when there is none.
func (f *Fleet) tlsListener() (string, string) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.tlsURL, f.tlsCertPEM
}

// selfSignedCert makes a throwaway certificate for the loopback addresses. A
// client that trusts it as its CA verifies the listener with it alone, since
// a trusted certificate is a complete chain by itself.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mcpchaos"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
