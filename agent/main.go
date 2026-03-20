// Bifrost Agent — a desktop system tray application that transparently intercepts
// AI API traffic using a TUN device and routes it through a Bifrost gateway.
//
// Architecture:
//
//	Real IP resolution → route hijacking via TUN → gVisor netstack →
//	SNI inspection → TLS MITM → header rewriting → Bifrost gateway
package main

import (
	"context"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/getlantern/systray"
	"github.com/maximhq/bifrost/agent/certinstall"
	"github.com/maximhq/bifrost/agent/config"
	"github.com/maximhq/bifrost/agent/internal/platform"
	"github.com/maximhq/bifrost/agent/lifecycle"
	"github.com/maximhq/bifrost/agent/tray"
)

func main() {
	var (
		gatewayURL    = flag.String("gateway", "", "Bifrost gateway URL (e.g. https://gateway.example.com)")
		virtualKey    = flag.String("virtual-key", "", "Virtual key for x-bf-vk header")
		managementURL = flag.String("management-url", "", "Bifrost management API URL for config sync")
		agentToken    = flag.String("agent-token", "", "Authentication token for management API")
		noTray        = flag.Bool("no-tray", false, "Run in headless mode (no system tray)")
		installCA     = flag.Bool("install-ca", false, "Generate and install the CA certificate, then exit")
		exportCA      = flag.String("export-ca", "", "Export the CA certificate PEM to this file path, then exit")
		verbose       = flag.Bool("verbose", false, "Enable verbose logging")
	)
	flag.Parse()

	if *verbose {
		log.SetFlags(log.Ltime | log.Lshortfile)
	} else {
		log.SetFlags(log.Ltime)
	}

	// Handle --export-ca and --install-ca before full startup
	if *exportCA != "" || *installCA {
		handleCACommands(*exportCA, *installCA)
		return
	}

	// Determine data directory
	dataDir, err := platform.AppDataDir()
	if err != nil {
		log.Fatalf("failed to get app data directory: %v", err)
	}
	log.Printf("data directory: %s", dataDir)

	// Create context for lifecycle management
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the agent (config, TUN, resolver, netstack, MITM proxy)
	agent, err := lifecycle.Start(ctx, lifecycle.StartupOptions{
		GatewayURL:    *gatewayURL,
		VirtualKey:    *virtualKey,
		ManagementURL: *managementURL,
		AgentToken:    *agentToken,
		DataDir:       dataDir,
	})
	if err != nil {
		log.Fatalf("startup failed: %v", err)
	}
	agent.Cancel = cancel

	// Start config sync in background if management URL is configured.
	// When server pushes new domains, update both the runtime config and resolver routes.
	if agent.SyncClient != nil {
		go agent.SyncClient.StartSync(ctx, agent.Config.PollInterval.Duration, func(cfg *config.AgentConfig) {
			agent.Config = cfg
			if agent.Store != nil {
				agent.Store.Save(cfg)
			}
			// Update domain map and resolver routes for new/removed domains
			added, removed := agent.Runtime.UpdateDomains(cfg.Domains)
			if len(added) > 0 || len(removed) > 0 {
				log.Printf("config sync: %d domains added, %d removed", len(added), len(removed))
				agent.Resolver.UpdateDomains(cfg.Domains)
			}
			log.Printf("config updated to version %d", cfg.ConfigVersion)
		})
	}

	// Start the connection router in background
	go func() {
		if err := agent.Router.Run(ctx); err != nil {
			log.Printf("router error: %v", err)
		}
	}()

	// Print status
	log.Printf("=== Bifrost Agent running ===")
	log.Printf("Gateway:     %s", agent.Config.GatewayURL)
	log.Printf("Virtual Key: %s", maskKey(agent.Config.VirtualKey))
	log.Printf("Intercepting %d AI domains (%d IP routes):", len(agent.Config.Domains), agent.Resolver.RouteCount())
	for _, d := range agent.Config.Domains {
		proxyFilter := "all paths"
		if len(d.ProxyPathPrefixes) > 0 {
			proxyFilter = fmt.Sprintf("only %v", d.ProxyPathPrefixes)
		}
		log.Printf("  MITM: %s → %s (%s)", d.Hostname, d.IntegrationPrefix, proxyFilter)
	}
	log.Printf("TUN:         %s", agent.TUN.Name)

	// Handle SIGINT/SIGTERM in both tray and headless modes.
	// This ensures routes are cleaned up on kill/Ctrl+C.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if *noTray {
		// Headless mode — wait for signal
		log.Printf("running in headless mode (no system tray)")
		<-sigCh
		agent.Shutdown()
	} else {
		// System tray mode
		app, events := tray.NewApp(tray.Config{
			GatewayURL:  agent.Config.GatewayURL,
			Enabled:     true,
			AutoStart:   lifecycle.IsAutoStartRegistered(),
			Version:     "0.1.0",
			CAInstalled: agent.IsCAInstalled(),
		})

		// Handle OS signals — shutdown and exit the tray
		go func() {
			<-sigCh
			log.Printf("received termination signal")
			agent.Shutdown()
			systray.Quit()
		}()

		// Handle tray events in background
		go func() {
			for {
				select {
				case enabled := <-events.Toggle:
					if enabled {
						log.Printf("interception enabled")
						app.SetStatus(tray.StatusConnected, "Status: Connected")
					} else {
						log.Printf("interception disabled")
						app.SetStatus(tray.StatusDisconnected, "Status: Disabled")
					}

				case <-events.InstallCA:
					log.Printf("installing CA certificate...")
					if err := agent.InstallCA(); err != nil {
						log.Printf("CA installation failed: %v", err)
						app.SetStatus(tray.StatusError, "CA install failed: "+err.Error())
					} else {
						log.Printf("CA certificate installed successfully")
						app.SetCAInstalled(true)
					}

				case autoStart := <-events.AutoStart:
					if autoStart {
						if err := lifecycle.RegisterAutoStart(); err != nil {
							log.Printf("auto-start registration failed: %v", err)
						} else {
							log.Printf("auto-start registered")
						}
					} else {
						if err := lifecycle.UnregisterAutoStart(); err != nil {
							log.Printf("auto-start removal failed: %v", err)
						} else {
							log.Printf("auto-start removed")
						}
					}

				case <-events.Quit:
					agent.Shutdown()
					systray.Quit()
					return
				}
			}
		}()

		// Run the tray (blocks on main thread — required by macOS)
		app.Run()
	}
}

// maskKey returns a masked version of an API key for logging.
func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// handleCACommands generates (or loads) the persisted CA and either exports or installs it.
// Uses the same loadOrGenerateCA as the agent startup so the certs always match.
func handleCACommands(exportPath string, install bool) {
	dataDir, err := platform.AppDataDir()
	if err != nil {
		log.Fatalf("failed to get app data directory: %v", err)
	}

	// Use the lifecycle package's CA loader — same one the agent uses at startup
	_, caTLSCert, err := lifecycle.LoadOrGenerateCA(dataDir)
	if err != nil {
		log.Fatalf("failed to load/generate CA: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caTLSCert.Certificate[0],
	})

	if exportPath != "" {
		if err := os.WriteFile(exportPath, certPEM, 0644); err != nil {
			log.Fatalf("failed to write CA cert: %v", err)
		}
		fmt.Printf("CA certificate exported to: %s\n", exportPath)
		fmt.Println("To install on macOS:")
		fmt.Printf("  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s\n", exportPath)
		return
	}

	if install {
		installer := certinstall.NewInstaller()
		fmt.Println("Installing CA certificate into system trust store...")
		if err := installer.Install(certPEM); err != nil {
			log.Fatalf("CA installation failed: %v", err)
		}
		fmt.Println("CA certificate installed successfully!")
		fmt.Printf("CA cert persisted at: %s\n", dataDir)
	}
}
