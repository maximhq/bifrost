// Package lifecycle manages the agent's startup sequence, graceful shutdown,
// and auto-start registration.
package lifecycle

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/maximhq/bifrost/agent/certinstall"
	"github.com/maximhq/bifrost/agent/config"
	"github.com/maximhq/bifrost/agent/proxy"
	"github.com/maximhq/bifrost/agent/tunnel"
)

// Agent holds all the running components of the Bifrost agent.
type Agent struct {
	Config        *config.AgentConfig
	Runtime       *config.RuntimeConfig
	Store         *config.Store
	SyncClient    *config.SyncClient
	TUN           *tunnel.TUNDevice
	NetStack      *tunnel.NetStack
	Resolver      *tunnel.DomainResolver
	Router        *tunnel.Router
	MITMProxy     *proxy.MITMProxy
	CertStore     *proxy.CertStore
	Forwarder     *proxy.Forwarder
	CertInstaller certinstall.Installer
	Cancel        context.CancelFunc
}

// StartupOptions configures the agent startup.
type StartupOptions struct {
	GatewayURL    string
	VirtualKey    string
	ManagementURL string
	AgentToken    string
	DataDir       string
}

// Start performs the full agent startup sequence:
//  1. Load or create configuration
//  2. Set up CA certificate and cert store
//  3. Create TUN device
//  4. Resolve AI domain IPs and add routes through TUN
//  5. Configure netstack
//  6. Start MITM proxy + router
func Start(ctx context.Context, opts StartupOptions) (*Agent, error) {
	agent := &Agent{
		CertInstaller: certinstall.NewInstaller(),
	}

	// Step 1: Create configuration (always start with defaults, override via CLI/server)
	store := config.NewStore(opts.DataDir)
	agent.Store = store

	// Always use fresh defaults — persisted config is only used when server-managed
	cfg := config.DefaultConfig()

	// Apply CLI overrides
	if opts.GatewayURL != "" {
		cfg.GatewayURL = opts.GatewayURL
	}
	if opts.VirtualKey != "" {
		cfg.VirtualKey = opts.VirtualKey
	}
	if opts.ManagementURL != "" {
		cfg.ManagementURL = opts.ManagementURL
	}
	if opts.AgentToken != "" {
		cfg.AgentToken = opts.AgentToken
	}

	// Try to sync from management API if configured
	if cfg.ManagementURL != "" && cfg.AgentToken != "" {
		syncClient := config.NewSyncClient(cfg.ManagementURL, cfg.AgentToken)
		agent.SyncClient = syncClient

		serverCfg, err := syncClient.FetchConfig(ctx)
		if err != nil {
			log.Printf("warning: config sync failed, using local config: %v", err)
		} else if serverCfg != nil {
			cfg = serverCfg
			if err := store.Save(cfg); err != nil {
				log.Printf("warning: failed to save config: %v", err)
			}
		}
	}

	agent.Config = cfg

	// Build runtime config
	runtime, err := config.NewRuntimeConfig(cfg)
	if err != nil {
		return nil, err
	}
	agent.Runtime = runtime

	// Step 2: Set up CA certificate
	if runtime.CACert != nil && runtime.CATLSCert != nil {
		agent.CertStore = proxy.NewCertStore(runtime.CACert, runtime.CATLSCert)
		log.Printf("using org-managed CA certificate")
	} else {
		// Try to load persisted CA from data dir, or generate a new one
		caCert, caTLSCert, err := LoadOrGenerateCA(opts.DataDir)
		if err != nil {
			return nil, err
		}
		agent.CertStore = proxy.NewCertStore(caCert, caTLSCert)
		// Store the CA in the runtime so InstallCA can access it
		runtime.CACert = caCert
		runtime.CATLSCert = caTLSCert
	}

	logCAStatus(agent.CertInstaller, runtime.CATLSCert)

	// Step 3: Create TUN device
	tun, err := tunnel.CreateTUN()
	if err != nil {
		return nil, err
	}
	agent.TUN = tun
	log.Printf("TUN device created: %s", tun.Name)

	if err := tun.ConfigureInterface(); err != nil {
		return nil, err
	}
	log.Printf("TUN interface configured: %s", tun.Name)

	// Step 4: Resolve AI domain IPs and add routes through TUN
	resolver := tunnel.NewDomainResolver(tun, runtime)
	agent.Resolver = resolver

	if err := resolver.ResolveAndRoute(); err != nil {
		return nil, err
	}
	log.Printf("resolved %d domains, %d IP routes active", resolver.ResolvedCount(), resolver.RouteCount())

	// Start periodic re-resolution to catch DNS changes
	go resolver.StartPeriodicRefresh(ctx, 30*time.Second)

	// Step 5: Create netstack
	ns, err := tunnel.NewNetStack(tun)
	if err != nil {
		return nil, err
	}
	agent.NetStack = ns

	if err := ns.ListenTCP(443); err != nil {
		return nil, err
	}
	log.Printf("netstack listening on port 443")

	// Step 6: Create MITM proxy and router
	mitmProxy := proxy.NewMITMProxy(agent.CertStore, cfg.GatewayURL, cfg.VirtualKey)
	agent.MITMProxy = mitmProxy

	router := tunnel.NewRouter(ns, runtime, func(conn net.Conn, hostname string, rule *config.DomainRule) {
		mitmProxy.HandleConnection(conn, hostname, rule)
	})
	agent.Router = router

	return agent, nil
}

// IsCAInstalled checks if the current CA is installed in the system trust store.
func (a *Agent) IsCAInstalled() bool {
	if a.Runtime.CATLSCert == nil {
		return false
	}
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: a.Runtime.CATLSCert.Certificate[0],
	})
	installed, err := a.CertInstaller.IsInstalled(certPEM)
	if err != nil {
		return false
	}
	return installed
}

// InstallCA installs the CA certificate into the system trust store.
func (a *Agent) InstallCA() error {
	if a.Runtime.CATLSCert == nil {
		return nil
	}
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: a.Runtime.CATLSCert.Certificate[0],
	})
	return a.CertInstaller.Install(certPEM)
}

const (
	caCertFile = "ca-cert.pem"
	caKeyFile  = "ca-key.pem"
)

// loadOrGenerateCA loads a persisted CA from dataDir, or generates a new one
// and saves it. This ensures the same CA is used across restarts so the
// installed CA cert remains valid.
func LoadOrGenerateCA(dataDir string) (*x509.Certificate, *tls.Certificate, error) {
	certPath := filepath.Join(dataDir, caCertFile)
	keyPath := filepath.Join(dataDir, caKeyFile)

	// Try to load existing CA
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	if certErr == nil && keyErr == nil {
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err == nil {
			x509Cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
			if err == nil {
				log.Printf("loaded persisted CA from %s", certPath)
				return x509Cert, &tlsCert, nil
			}
		}
		log.Printf("warning: persisted CA is invalid, regenerating")
	}

	// Generate new CA
	caCert, caTLSCert, err := proxy.GenerateSelfSignedCA()
	if err != nil {
		return nil, nil, err
	}

	// Persist it
	os.MkdirAll(dataDir, 0700)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caTLSCert.Certificate[0]})
	if ecKey, ok := caTLSCert.PrivateKey.(*ecdsa.PrivateKey); ok {
		keyDER, err := x509.MarshalECPrivateKey(ecKey)
		if err != nil {
			log.Printf("warning: could not persist CA key: %v", err)
			return caCert, caTLSCert, nil
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	} else {
		keyBytes, err := x509.MarshalPKCS8PrivateKey(caTLSCert.PrivateKey)
		if err != nil {
			log.Printf("warning: could not persist CA key: %v", err)
			return caCert, caTLSCert, nil
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	}

	os.WriteFile(certPath, certPEM, 0644)
	os.WriteFile(keyPath, keyPEM, 0600)
	log.Printf("generated and persisted new CA to %s", certPath)

	return caCert, caTLSCert, nil
}

func logCAStatus(installer certinstall.Installer, tlsCert *tls.Certificate) {
	if tlsCert == nil || len(tlsCert.Certificate) == 0 {
		return
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: tlsCert.Certificate[0],
	})

	fingerprint, err := certinstall.CertFingerprint(certPEM)
	if err != nil {
		log.Printf("warning: could not compute CA fingerprint: %v", err)
	} else if len(fingerprint) >= 16 {
		log.Printf("active CA fingerprint: %s…%s", fingerprint[:8], fingerprint[len(fingerprint)-8:])
	}

	installed, err := installer.IsInstalled(certPEM)
	if err != nil {
		log.Printf("warning: could not check CA installation status: %v", err)
		return
	}
	if !installed {
		log.Printf("CA certificate is NOT installed in system trust store")
		log.Printf("use the tray menu or run with --install-ca to install it")
		return
	}
	log.Printf("CA certificate is installed in system trust store")
}
