// Package tray implements the system tray UI for the Bifrost agent using
// getlantern/systray. It shows connection status, allows toggling interception
// on/off, displays intercepted domains, and provides CA installation.
package tray

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/getlantern/systray"
)

// Status represents the agent's current state.
type Status int

const (
	StatusDisconnected Status = iota
	StatusConnecting
	StatusConnected
	StatusError
	StatusWarning
)

// Events carries channels for tray menu actions.
type Events struct {
	// Toggle is signaled when the user clicks Enable/Disable.
	Toggle chan bool

	// InstallCA is signaled when the user clicks "Install CA Certificate".
	InstallCA chan struct{}

	// Quit is signaled when the user clicks Quit.
	Quit chan struct{}

	// AutoStart is signaled when the user toggles "Start on Login".
	AutoStart chan bool
}

// Config holds the initial tray configuration.
type Config struct {
	GatewayURL  string
	Enabled     bool
	AutoStart   bool
	Version     string
	CAInstalled bool
}

// App manages the system tray lifecycle and menu items.
type App struct {
	cfg    Config
	events Events

	// Menu items we need to update dynamically
	statusItem    *systray.MenuItem
	gatewayItem   *systray.MenuItem
	toggleItem    *systray.MenuItem
	caItem        *systray.MenuItem
	autoStartItem *systray.MenuItem

	// Animation state for the "connecting" spinner
	animMu     sync.Mutex
	animCancel chan struct{} // closed to stop animation goroutine
}

// NewApp creates a tray application with the given config.
func NewApp(cfg Config) (*App, Events) {
	events := Events{
		Toggle:    make(chan bool, 1),
		InstallCA: make(chan struct{}, 1),
		Quit:      make(chan struct{}, 1),
		AutoStart: make(chan bool, 1),
	}
	return &App{
		cfg:    cfg,
		events: events,
	}, events
}

// Run starts the system tray. This must be called from the main goroutine on macOS.
// It blocks until Quit is selected.
func (a *App) Run() {
	systray.Run(a.onReady, a.onQuit)
}

// SetStatus updates the tray icon and status text.
// On macOS, uses SetTemplateIcon for automatic dark/light mode adaptation.
// On Linux/Windows, uses colored icons.
// For StatusConnecting, runs a frame-cycling animation similar to Tailscale's
// connecting state — the icon fades in and out to indicate activity.
func (a *App) SetStatus(status Status, message string) {
	// Stop any running animation before changing state
	a.stopAnimation()

	switch status {
	case StatusConnected:
		// Template icon for macOS (monochrome, auto dark/light mode)
		// Falls back to color icon on other platforms
		systray.SetTemplateIcon(TemplateIconConnected, IconConnected)
		systray.SetTooltip("Bifrost Agent — Connected")
	case StatusDisconnected:
		systray.SetTemplateIcon(TemplateIconDisconnected, IconDisconnected)
		systray.SetTooltip("Bifrost Agent — Disabled")
	case StatusError:
		// Error/warning: use color icon even on macOS for visibility
		systray.SetIcon(IconError)
		systray.SetTooltip("Bifrost Agent — Error")
	case StatusWarning:
		systray.SetIcon(IconWarning)
		systray.SetTooltip("Bifrost Agent — Warning")
	case StatusConnecting:
		systray.SetTooltip("Bifrost Agent — Connecting...")
		a.startAnimation()
	}

	if a.statusItem != nil && message != "" {
		a.statusItem.SetTitle(message)
	}
}

// SetEnabled updates the toggle state from the server side.
// This allows the server to remotely enable/disable interception.
func (a *App) SetEnabled(enabled bool) {
	if a.toggleItem == nil {
		return
	}
	if enabled {
		a.toggleItem.Check()
		a.SetStatus(StatusConnected, "Status: Connected")
	} else {
		a.toggleItem.Uncheck()
		a.SetStatus(StatusDisconnected, "Status: Disabled")
	}
}

// SetCAInstalled updates the CA installation menu item.
func (a *App) SetCAInstalled(installed bool) {
	if a.caItem == nil {
		return
	}
	if installed {
		a.caItem.SetTitle("CA Certificate: Installed")
		a.caItem.Disable()
	} else {
		a.caItem.SetTitle("Install CA Certificate")
		a.caItem.Enable()
	}
}

func (a *App) onReady() {
	// Set initial icon (template icon for macOS dark mode, color fallback for others)
	if a.cfg.Enabled {
		systray.SetTemplateIcon(TemplateIconConnected, IconConnected)
		systray.SetTooltip("Bifrost Agent — Connected")
	} else {
		systray.SetTemplateIcon(TemplateIconDisconnected, IconDisconnected)
		systray.SetTooltip("Bifrost Agent — Disabled")
	}
	systray.SetTitle("") // No title text next to icon on macOS

	// Status section — set based on initial enabled state
	if a.cfg.Enabled {
		a.statusItem = systray.AddMenuItem("Status: Connected", "Current agent status")
	} else {
		a.statusItem = systray.AddMenuItem("Status: Disabled", "Current agent status")
	}
	a.statusItem.Disable()

	a.gatewayItem = systray.AddMenuItem("Gateway: "+a.cfg.GatewayURL, "Bifrost gateway URL")
	a.gatewayItem.Disable()

	systray.AddSeparator()

	// Toggle
	if a.cfg.Enabled {
		a.toggleItem = systray.AddMenuItemCheckbox("Enabled", "Toggle traffic interception", true)
	} else {
		a.toggleItem = systray.AddMenuItemCheckbox("Enabled", "Toggle traffic interception", false)
	}

	// CA certificate
	if a.cfg.CAInstalled {
		a.caItem = systray.AddMenuItem("CA Certificate: Installed", "CA certificate status")
		a.caItem.Disable()
	} else {
		a.caItem = systray.AddMenuItem("Install CA Certificate", "Install CA certificate in system trust store")
	}

	systray.AddSeparator()

	// Auto-start
	a.autoStartItem = systray.AddMenuItemCheckbox("Start on Login", "Automatically start on login", a.cfg.AutoStart)

	// About
	aboutItem := systray.AddMenuItem(fmt.Sprintf("Bifrost Agent v%s", a.cfg.Version), "About")
	aboutItem.Disable()

	systray.AddSeparator()

	// Quit
	quitItem := systray.AddMenuItem("Quit", "Quit Bifrost Agent")

	// Handle menu clicks in a goroutine
	go func() {
		for {
			select {
			case <-a.toggleItem.ClickedCh:
				enabled := a.toggleItem.Checked()
				// Toggle state: if it was checked, now unchecked and vice versa
				if enabled {
					a.toggleItem.Uncheck()
					a.events.Toggle <- false
				} else {
					a.toggleItem.Check()
					a.events.Toggle <- true
				}

			case <-a.caItem.ClickedCh:
				select {
				case a.events.InstallCA <- struct{}{}:
				default:
				}

			case <-a.autoStartItem.ClickedCh:
				autoStart := a.autoStartItem.Checked()
				if autoStart {
					a.autoStartItem.Uncheck()
					a.events.AutoStart <- false
				} else {
					a.autoStartItem.Check()
					a.events.AutoStart <- true
				}

			case <-quitItem.ClickedCh:
				select {
				case a.events.Quit <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	log.Printf("system tray ready")
}

func (a *App) onQuit() {
	a.stopAnimation()
	log.Printf("system tray exiting")
}

// startAnimation begins a connecting animation that cycles the icon opacity
// in a pulsing pattern, similar to Tailscale's connecting state.
// The icon fades between full and dim opacity on a 600ms cycle.
func (a *App) startAnimation() {
	a.animMu.Lock()
	defer a.animMu.Unlock()

	a.animCancel = make(chan struct{})
	cancel := a.animCancel

	go func() {
		// Pre-generate animation frames at different opacity levels.
		// 6 frames: fade from dim → bright → dim over ~1.2 seconds.
		frames := ConnectingAnimationFrames
		frameIndex := 0

		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		// Set first frame immediately
		systray.SetTemplateIcon(frames[0], IconWarning)

		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				frameIndex = (frameIndex + 1) % len(frames)
				systray.SetTemplateIcon(frames[frameIndex], IconWarning)
			}
		}
	}()
}

// stopAnimation stops the connecting animation if it's running.
func (a *App) stopAnimation() {
	a.animMu.Lock()
	defer a.animMu.Unlock()

	if a.animCancel != nil {
		close(a.animCancel)
		a.animCancel = nil
	}
}
