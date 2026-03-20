package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"sync"
	"time"
)

// CertStore is a thread-safe LRU cache of per-domain TLS certificates.
// Certificates are generated on demand and cached until they expire.
type CertStore struct {
	mu        sync.RWMutex
	certs     map[string]*certEntry
	caCert    *x509.Certificate
	caTLSCert *tls.Certificate
	maxSize   int
}

type certEntry struct {
	cert    *tls.Certificate
	expires time.Time
}

// NewCertStore creates a certificate store backed by the given CA.
func NewCertStore(caCert *x509.Certificate, caTLSCert *tls.Certificate) *CertStore {
	return &CertStore{
		certs:     make(map[string]*certEntry),
		caCert:    caCert,
		caTLSCert: caTLSCert,
		maxSize:   1000,
	}
}

// GetOrCreate returns a cached certificate for the domain, or generates a new
// one if the cache is empty or the existing cert is expiring soon.
func (cs *CertStore) GetOrCreate(domain string) (*tls.Certificate, error) {
	cs.mu.RLock()
	entry, ok := cs.certs[domain]
	cs.mu.RUnlock()

	// Use cached cert if it's valid for at least 1 more hour
	if ok && time.Now().Add(time.Hour).Before(entry.expires) {
		return entry.cert, nil
	}

	// Generate a new certificate
	cert, err := GenerateCert(domain, cs.caCert, cs.caTLSCert)
	if err != nil {
		return nil, err
	}

	cs.mu.Lock()
	// Evict if we've exceeded max size (simple: clear all and start fresh)
	if len(cs.certs) >= cs.maxSize {
		cs.certs = make(map[string]*certEntry)
	}
	cs.certs[domain] = &certEntry{
		cert:    cert,
		expires: time.Now().Add(24 * time.Hour),
	}
	cs.mu.Unlock()

	return cert, nil
}

// UpdateCA replaces the CA certificate used for signing new domain certs.
// Existing cached certs remain valid until they expire.
func (cs *CertStore) UpdateCA(caCert *x509.Certificate, caTLSCert *tls.Certificate) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.caCert = caCert
	cs.caTLSCert = caTLSCert
	// Clear cache to force re-generation with new CA
	cs.certs = make(map[string]*certEntry)
}
