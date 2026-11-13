// Run with: go run tests/e2e/api/runners/mcpoauthtlsfixture.go
// The certificate and private key exist only in memory. Both listeners are loopback-only.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"
)

func main() {
	port := flag.Int("port", 8821, "HTTPS issuer port")
	controlPort := flag.Int("control-port", 8820, "HTTP fixture info port")
	flag.Parse()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "MCP OAuth test issuer"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	origin := fmt.Sprintf("https://127.0.0.1:%d", *port)
	var registrations, exchanges atomic.Int32
	jsonReply := func(w http.ResponseWriter, status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/metadata"`, origin))
		jsonReply(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusOK, map[string]any{"resource": origin + "/mcp", "authorization_servers": []string{origin}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusOK, map[string]any{
			"issuer": origin, "authorization_endpoint": origin + "/authorize",
			"token_endpoint": origin + "/token", "registration_endpoint": origin + "/register",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		registrations.Add(1)
		jsonReply(w, http.StatusCreated, map[string]string{"client_id": "tls-fixture-client", "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		jsonReply(w, http.StatusOK, map[string]any{"access_token": "tls-fixture-access", "refresh_token": "tls-fixture-refresh", "token_type": "Bearer", "expires_in": 3600})
	})
	control := http.NewServeMux()
	control.HandleFunc("GET /__info", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusOK, map[string]any{"origin": origin, "ca_cert_pem": ca, "registrations": registrations.Load(), "exchanges": exchanges.Load()})
	})
	issuer := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}}
	info := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *controlPort), Handler: control, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fail := make(chan error, 2)
	go func() { fail <- issuer.ListenAndServeTLS("", "") }()
	go func() { fail <- info.ListenAndServe() }()
	log.Printf("MCP OAuth TLS fixture: %s; public certificate at http://%s/__info", origin, info.Addr)
	select {
	case <-ctx.Done():
	case err := <-fail:
		log.Print(err)
	}
	_ = issuer.Close()
	_ = info.Close()
}
