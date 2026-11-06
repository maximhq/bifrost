package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/cli/internal/apis"
	"github.com/maximhq/bifrost/cli/internal/browserauth"
	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
	"github.com/maximhq/bifrost/cli/internal/installer"
	"github.com/maximhq/bifrost/cli/internal/mcp"
	"github.com/maximhq/bifrost/cli/internal/runtime"
	"github.com/maximhq/bifrost/cli/internal/secrets"
	"github.com/maximhq/bifrost/cli/internal/sessionauth"
	"github.com/maximhq/bifrost/cli/internal/ui/logo"
	"github.com/maximhq/bifrost/cli/internal/ui/tui"
	"github.com/maximhq/bifrost/cli/internal/update"
	"golang.org/x/term"
)

// Options holds the CLI flags and build metadata passed to the application.
type Options struct {
	Version  string
	Commit   string
	NoResume bool
	Tabs     bool
	Config   string
	Worktree string
}

// App is the main Bifrost CLI application. It manages configuration, state,
// and the interactive TUI loop for selecting and launching harnesses.
type App struct {
	in      io.Reader
	out     io.Writer
	errOut  io.Writer
	opts    Options
	state   *config.State
	cfgFile *config.FileConfig
	// sessionStore and httpClient are injectable seams for the SSO-backed model
	// discovery path; production uses the OS keyring and default HTTP transport.
	sessionStore sessionauth.SessionStore
	httpClient   *http.Client
	openBrowser  func(string) error

	statePath    string
	configPath   string
	configSource string
	bootHeader   string
}

// New creates a new App instance with the given I/O streams and options.
func New(in io.Reader, out, errOut io.Writer, opts Options) *App {
	return &App{
		in:     in,
		out:    out,
		errOut: errOut,
		opts:   opts,
	}
}

// Run starts the interactive TUI loop. It loads config and state, then presents
// the chooser, launches the selected harness with native terminal passthrough
// by default, and returns to the chooser when the harness exits. Tabs remains
// available as an opt-in compatibility mode.
func (a *App) Run(ctx context.Context) error {
	if err := a.loadStateAndConfig(); err != nil {
		return err
	}

	updateCh := update.CheckInBackground(a.opts.Version, a.statePath)

	activeProfile := a.getOrCreateProfile()
	if activeProfile == nil {
		return errors.New("failed to initialize profile")
	}

	vk, err := secrets.GetVirtualKey(activeProfile.ID)
	if err != nil {
		fmt.Fprintf(a.errOut, "warning: %v\n", err)
	}
	if vk == "" && a.cfgFile != nil && strings.TrimSpace(a.cfgFile.VirtualKey) != "" {
		if err := secrets.SetVirtualKey(activeProfile.ID, strings.TrimSpace(a.cfgFile.VirtualKey)); err == nil {
			vk = strings.TrimSpace(a.cfgFile.VirtualKey)
			a.cfgFile.VirtualKey = ""
			if a.configPath != "" {
				if err := config.SaveConfig(a.configPath, a.cfgFile); err != nil {
					fmt.Fprintf(a.errOut, "warning: save config after key migration: %v\n", err)
				}
			}
		} else {
			fmt.Fprintf(a.errOut, "warning: %v\n", err)
		}
	}

	selection := a.state.Selections[activeProfile.ID]
	if a.opts.NoResume {
		selection = config.Selection{}
	}

	// Seed defaults from config if state has no selection
	if a.cfgFile != nil {
		if selection.Harness == "" {
			selection.Harness = strings.TrimSpace(a.cfgFile.DefaultHarness)
		}
		if selection.Model == "" {
			selection.Model = strings.TrimSpace(a.cfgFile.DefaultModel)
		}
	}

	worktree := strings.TrimSpace(a.opts.Worktree)
	var updateVersion string
	enterpriseSSOAvailability := make(map[string]bool)
	enterpriseSSOChecked := make(map[string]bool)
	resolveEnterpriseSSOAvailability := func(checkCtx context.Context, baseURL string) bool {
		baseURL = strings.TrimSpace(baseURL)
		if enterpriseSSOChecked[baseURL] {
			return enterpriseSSOAvailability[baseURL]
		}
		available := a.enterpriseSSOAvailable(checkCtx, baseURL)
		enterpriseSSOChecked[baseURL] = true
		enterpriseSSOAvailability[baseURL] = available
		return available
	}

	// chooseAndPrepare runs the chooser TUI, handles installation flows,
	// persists state, and returns a launch spec. Loops internally until
	// the user picks a valid harness or quits.
	chooseAndPrepare := func(chooserCtx context.Context, notify func(runtime.TabNoticeLevel, string), tabBarLine func() string, stdinReader io.Reader, msg string, isAfterSession bool, seed *runtime.LaunchSpec) (*runtime.LaunchSpec, error) {
		seedApplied := false
		for {
			harnesses := a.harnessOptions()
			baseURL := activeProfile.BaseURL
			currentVK := vk
			currentSelection := selection
			currentWorktree := worktree
			if seed != nil && !seedApplied {
				baseURL = seed.BaseURL
				currentVK = seed.VirtualKey
				currentSelection.Harness = seed.Harness.ID
				currentSelection.Model = seed.Model
				currentWorktree = seed.Worktree
				seedApplied = true
			}
			enterpriseSSOAvailable := resolveEnterpriseSSOAvailability(chooserCtx, baseURL)
			agentToken, tokenErr := a.agentSessionStore().Get(activeProfile.ID, secrets.AgentToken)
			if tokenErr != nil {
				_, _ = fmt.Fprintf(a.errOut, "warning: load Enterprise SSO session: %v\n", tokenErr)
			}
			agentIdentity := ""
			if strings.TrimSpace(agentToken) != "" {
				identity, identityErr := sessionauth.StoredUserLabel(a.agentSessionStore(), activeProfile.ID)
				if identityErr != nil {
					_, _ = fmt.Fprintf(a.errOut, "warning: load Enterprise SSO identity: %v\n", identityErr)
				} else {
					agentIdentity = identity
				}
			}

			reservedRows := 0
			if tabBarLine != nil {
				reservedRows = 1
			}
			choice, err := tui.RunChooser(tui.ChooserConfig{
				Version:                a.opts.Version,
				Commit:                 a.opts.Commit,
				ConfigSrc:              a.configSource,
				Message:                msg,
				UpdateVersion:          updateVersion,
				BaseURL:                baseURL,
				VirtualKey:             currentVK,
				EnterpriseSSOAvailable: enterpriseSSOAvailable,
				AgentSignedIn: strings.TrimSpace(agentToken) != "" &&
					strings.TrimSpace(baseURL) == strings.TrimSpace(activeProfile.BaseURL),
				AgentIdentity: agentIdentity,
				Harness:       currentSelection.Harness,
				Model:         currentSelection.Model,
				Worktree:      currentWorktree,
				AfterSession:  isAfterSession,
				ReservedRows:  reservedRows,
				Harnesses:     harnesses,
				TabBarLine:    tabBarLine,
				FetchModels: func(fetchCtx context.Context, requestedBaseURL, requestedVirtualKey string) ([]string, error) {
					return a.listModels(fetchCtx, activeProfile.ID, activeProfile.BaseURL, requestedBaseURL, requestedVirtualKey)
				},
				Input: stdinReader,
				Notify: func(message string, isError bool) {
					level := runtime.TabNoticeInfo
					if isError {
						level = runtime.TabNoticeError
					}
					if notify != nil {
						notify(level, message)
					}
				},
			})
			if err != nil {
				return nil, err
			}
			if choice.BackToTabs {
				return nil, runtime.ErrBackToTabs
			}
			if choice.UpdateRequested {
				return nil, runtime.ErrUpdateRequested
			}
			if choice.Quit {
				return nil, nil
			}

			previousBaseURL := strings.TrimSpace(activeProfile.BaseURL)
			newBaseURL := strings.TrimSpace(choice.BaseURL)
			if newBaseURL != previousBaseURL {
				// A session belongs to the origin where it was issued. Revoke it
				// against the old gateway and clear local state before persisting
				// the edited URL, so a later chooser cycle cannot reuse it.
				if logoutErr := a.updateProfileBaseURL(ctx, activeProfile, newBaseURL); logoutErr != nil {
					var revocationErr *sessionauth.RevocationError
					if !errors.As(logoutErr, &revocationErr) {
						msg = "Could not change gateway URL: " + logoutErr.Error()
						isAfterSession = false
						continue
					}
					_, _ = fmt.Fprintf(a.errOut, "warning: %v\n", logoutErr)
				}
			} else {
				activeProfile.BaseURL = newBaseURL
			}
			selection.Harness = strings.TrimSpace(choice.Harness)
			selection.Model = strings.TrimSpace(choice.Model)
			vk = strings.TrimSpace(choice.VirtualKey)
			worktree = strings.TrimSpace(choice.Worktree)

			if choice.LoginRequested || choice.LogoutRequested {
				a.persistChooserPreferences(activeProfile, selection, vk)
				if choice.LoginRequested {
					identity, loginErr := a.loginEnterpriseSSO(ctx, activeProfile.ID, activeProfile.BaseURL)
					if loginErr != nil {
						msg = "Enterprise SSO sign-in failed: " + loginErr.Error()
					} else if identity != "" {
						msg = "Signed in with Enterprise SSO as " + identity
					} else {
						msg = "Signed in with Enterprise SSO"
					}
				} else if logoutErr := a.enterpriseAuthenticator(activeProfile.ID, activeProfile.BaseURL).Logout(ctx); logoutErr != nil {
					msg = logoutErr.Error()
				} else {
					msg = "Signed out of Enterprise SSO"
				}
				isAfterSession = false
				continue
			}

			h, ok := harness.Get(selection.Harness)
			if !ok {
				msg = "invalid harness selected"
				isAfterSession = false
				continue
			}

			// Handle install request
			if choice.InstallHarness {
				cmd, args := installer.InstallCommand(h)
				shouldInstall, err := tui.RunConfirmInstall(a.bootHeader, h.Label, cmd+" "+strings.Join(args, " "))
				if err != nil {
					return nil, err
				}
				if !shouldInstall {
					msg = h.Label + " installation skipped"
					continue
				}
				if err := installer.EnsureNPM(); err != nil {
					msg = err.Error()
					continue
				}
				fmt.Fprintf(a.out, "\nInstalling %s...\n", h.Label)
				if err := installer.RunInstall(ctx, a.out, a.errOut, h); err != nil {
					msg = err.Error()
					continue
				}
				if !installer.IsInstalled(h) {
					msg = h.Label + " installed but binary still not in PATH"
					continue
				}
				msg = h.Label + " installed successfully"
				continue
			}

			a.persistChooserPreferences(activeProfile, selection, vk)

			mcp.AttachBestEffort(ctx, a.out, a.errOut, h, activeProfile.BaseURL, vk)
			agentToken, tokenErr = a.agentSessionStore().Get(activeProfile.ID, secrets.AgentToken)
			if tokenErr != nil {
				fmt.Fprintf(a.errOut, "warning: load Enterprise SSO session: %v\n", tokenErr)
				agentToken = ""
			}
			agentToken = launchAgentToken(agentToken, activeProfile.BaseURL, choice.BaseURL)
			tokenHelperCommand, authErr := runtime.PrepareLaunchAuthentication(h, agentToken, vk)
			if authErr != nil {
				msg = authErr.Error()
				isAfterSession = false
				continue
			}

			return &runtime.LaunchSpec{
				Harness: h, BaseURL: activeProfile.BaseURL, VirtualKey: vk, AgentToken: agentToken,
				Context: activeProfile.ID, StatePath: a.statePath, TokenHelperCommand: tokenHelperCommand,
				Model: selection.Model, Worktree: worktree,
			}, nil
		}
	}

	// Main loop — each iteration opens the chooser, gives the selected harness
	// direct access to the terminal, then returns to the chooser after exit.
	message := ""
	afterSession := false

	// Wait for update check to complete (up to 4s — the HTTP request has a 3s timeout).
	select {
	case result := <-updateCh:
		if result != nil && result.UpdateAvailable {
			updateVersion = result.LatestVersion
			a.state.LastVersionCheck = result.CheckedAt
			a.state.LastKnownVersion = result.LatestVersion
			_ = config.SaveState(a.statePath, a.state) // best-effort
		}
		updateCh = nil
	case <-time.After(4 * time.Second):
	}

	if updateVersion != "" {
		shouldUpdate, err := tui.RunConfirmUpdateIO(a.bootHeader, updateVersion, a.in, a.out)
		if err != nil {
			return err
		}
		if shouldUpdate {
			if err := update.RunSelfUpdate(a.opts.Version); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}
			execPath, err := os.Executable()
			if err != nil {
				fmt.Fprintf(a.out, "Updated successfully. Please restart bifrost.\n")
				return nil
			}
			return reexecSelf(execPath, os.Args, os.Environ())
		}
		updateVersion = ""
	}

	for {
		launched := false
		if a.opts.Tabs {
			err = runtime.RunTabbed(ctx, a.out, a.errOut, a.opts.Version, updateVersion, func(sessionCtx context.Context, notify func(runtime.TabNoticeLevel, string), tabBarLine func() string, stdinReader io.Reader, seed *runtime.LaunchSpec) (*runtime.LaunchSpec, error) {
				return chooseAndPrepare(sessionCtx, notify, tabBarLine, stdinReader, message, afterSession, seed)
			})
		} else {
			launched, err = runNativeSession(ctx, a.out, a.errOut, func(sessionCtx context.Context, notify func(runtime.TabNoticeLevel, string), tabBarLine func() string, stdinReader io.Reader, seed *runtime.LaunchSpec) (*runtime.LaunchSpec, error) {
				return chooseAndPrepare(sessionCtx, notify, tabBarLine, stdinReader, message, afterSession, seed)
			}, runtime.RunInteractive)
		}

		if errors.Is(err, runtime.ErrUpdateRequested) {
			if err := update.RunSelfUpdate(a.opts.Version); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}
			// Re-exec with the updated binary.
			execPath, err := os.Executable()
			if err != nil {
				fmt.Fprintf(a.out, "Updated successfully. Please restart bifrost.\n")
				return nil
			}
			return reexecSelf(execPath, os.Args, os.Environ())
		}

		if errors.Is(err, runtime.ErrQuit) {
			return nil
		}
		if launched {
			afterSession = true
			message = ""
			if err != nil {
				message = err.Error()
			}
			continue
		}
		if err != nil {
			return err
		}

		message = ""
		afterSession = true
	}
}

type interactiveSessionRunner func(context.Context, io.Writer, io.Writer, runtime.LaunchSpec) error

// runNativeSession opens one chooser and gives the selected harness direct PTY
// passthrough. The bool reports whether a harness was launched, allowing the
// caller to return session failures to the chooser instead of exiting Bifrost.
func runNativeSession(ctx context.Context, out, errOut io.Writer, choose runtime.NewTabFunc, run interactiveSessionRunner) (bool, error) {
	spec, err := choose(ctx, nil, nil, nil, nil)
	if err != nil {
		return false, err
	}
	if spec == nil {
		return false, runtime.ErrQuit
	}
	return true, run(ctx, out, errOut, *spec)
}

func (a *App) agentSessionStore() sessionauth.SessionStore {
	if a.sessionStore != nil {
		return a.sessionStore
	}
	return secrets.Keyring{}
}

// browserAuthClient creates the same hardened HTTP transport used by the
// non-interactive CLI before any SSO credential is sent.
func (a *App) browserAuthClient(baseURL string) *browserauth.Client {
	api := client.New(baseURL, client.Credentials{}, 30*time.Second)
	if a.httpClient != nil {
		api.HTTPClient = a.httpClient
	}
	userAgent := "bifrost-cli/" + a.opts.Version
	return &browserauth.Client{
		BaseURL: baseURL, HTTPClient: api.HTTPClient, UserAgent: userAgent,
		Version: a.opts.Version, OpenBrowser: a.openBrowser,
	}
}

// enterpriseSSOAvailable reports whether the gateway explicitly advertises
// browser-based agent authentication. Capability discovery is deliberately
// short so an unavailable Enterprise endpoint cannot stall the launch TUI.
func (a *App) enterpriseSSOAvailable(ctx context.Context, baseURL string) bool {
	if strings.TrimSpace(baseURL) == "" {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	status, err := a.browserAuthClient(baseURL).CheckStatus(checkCtx)
	return err == nil && status.IdPConfigured
}

func (a *App) enterpriseAuthenticator(profileID, baseURL string) sessionauth.Authenticator {
	return sessionauth.Authenticator{
		Store: a.agentSessionStore(), ProfileID: profileID, Client: a.browserAuthClient(baseURL),
	}
}

// updateProfileBaseURL invalidates the session at its issuing gateway before
// making the edited URL the profile's trusted destination. Local credentials
// are cleared even if remote revocation fails.
func (a *App) updateProfileBaseURL(ctx context.Context, profile *config.Profile, baseURL string) error {
	previousBaseURL := strings.TrimSpace(profile.BaseURL)
	newBaseURL := strings.TrimSpace(baseURL)
	if newBaseURL == previousBaseURL {
		return nil
	}
	err := a.enterpriseAuthenticator(profile.ID, previousBaseURL).Logout(ctx)
	if err != nil {
		var revocationErr *sessionauth.RevocationError
		if !errors.As(err, &revocationErr) {
			return err
		}
	}
	profile.BaseURL = newBaseURL
	return err
}

// loginEnterpriseSSO runs browser PKCE outside the full-screen chooser. The
// chooser is reopened after completion with the updated profile-scoped state.
func (a *App) loginEnterpriseSSO(ctx context.Context, profileID, baseURL string) (string, error) {
	authenticator := a.enterpriseAuthenticator(profileID, baseURL)
	authenticator.Client.Authorization = func(target string) {
		_, _ = fmt.Fprintln(a.errOut, "Opening your browser to sign in to Bifrost...")
		_, _ = fmt.Fprintln(a.errOut, "If the browser does not open, use this URL:")
		_, _ = fmt.Fprintln(a.errOut, target)
		_, _ = fmt.Fprintln(a.errOut, "Waiting for authentication...")
	}
	authenticator.Client.Warning = func(err error) {
		_, _ = fmt.Fprintf(a.errOut, "Warning: %v\n", err)
	}
	response, err := authenticator.SignIn(ctx, false)
	if err != nil {
		return "", err
	}
	return sessionauth.UserLabel(response.User), nil
}

func (a *App) persistChooserPreferences(profile *config.Profile, selection config.Selection, virtualKey string) {
	if err := secrets.SetVirtualKey(profile.ID, virtualKey); err != nil {
		_, _ = fmt.Fprintf(a.errOut, "warning: %v\n", err)
	}
	a.state.LastProfileID = profile.ID
	a.state.Selections[profile.ID] = selection
	if err := config.SaveState(a.statePath, a.state); err != nil {
		_, _ = fmt.Fprintf(a.errOut, "warning: %v\n", err)
	}
	if a.cfgFile == nil {
		a.cfgFile = &config.FileConfig{}
	}
	a.cfgFile.BaseURL = profile.BaseURL
	a.cfgFile.DefaultHarness = selection.Harness
	a.cfgFile.DefaultModel = selection.Model
	if a.configPath != "" {
		if err := config.SaveConfig(a.configPath, a.cfgFile); err != nil {
			_, _ = fmt.Fprintf(a.errOut, "warning: save config: %v\n", err)
		}
	}
}

// listModels gives the chooser the same SSO-first authentication and refresh
// semantics as non-interactive CLI inference commands.
// listModels fetches available models for baseURL. The Enterprise SSO agent
// bearer is only attached when baseURL matches the profile's configured
// (trusted) base URL — never to an edited or typo'd URL the user has not
// confirmed, since that would leak a live credential to an arbitrary host.
func (a *App) listModels(ctx context.Context, profileID, trustedBaseURL, baseURL, virtualKey string) ([]string, error) {
	store := a.agentSessionStore()
	credentials := client.Credentials{VirtualKey: virtualKey}
	if strings.TrimSpace(baseURL) == strings.TrimSpace(trustedBaseURL) {
		agentToken, err := store.Get(profileID, secrets.AgentToken)
		if err != nil {
			return nil, err
		}
		selectedID, err := store.Get(profileID, secrets.AgentVirtualKeyID)
		if err != nil {
			return nil, err
		}
		credentials.AgentToken = agentToken
		credentials.AgentVirtualKeyID = selectedID
	}
	api := client.New(baseURL, credentials, 20*time.Second)
	if a.httpClient != nil {
		api.HTTPClient = a.httpClient
	}
	api.UserAgent = "bifrost-cli/" + a.opts.Version
	browserClient := &browserauth.Client{
		BaseURL: baseURL, HTTPClient: api.HTTPClient, UserAgent: api.UserAgent, Version: a.opts.Version,
	}
	refresher := sessionauth.Refresher{
		Store: store, ProfileID: profileID, StatePath: a.statePath, Client: browserClient,
	}
	api.RefreshAgentToken = refresher.Refresh
	response, err := api.Do(ctx, client.Request{Path: "/v1/models", Auth: client.AuthInference})
	if err != nil {
		return nil, err
	}
	return apis.ParseModels(response.Body)
}

// launchAgentToken returns the Enterprise SSO agent token to carry into a
// LaunchSpec, or empty if the launch's base URL was edited away from the
// profile's trusted origin. A launched coding agent sends every inference
// request — including this bearer token — to its configured base URL for
// the whole session, so carrying it to an edited/unvalidated URL would leak
// a live credential (CWE-522). PrepareLaunchAuthentication then naturally
// falls back to the virtual key alone when this returns empty.
func launchAgentToken(agentToken, trustedBaseURL, launchBaseURL string) string {
	if strings.TrimSpace(launchBaseURL) != strings.TrimSpace(trustedBaseURL) {
		return ""
	}
	return agentToken
}

// loadStateAndConfig loads configuration from saved state from the last run
func (a *App) loadStateAndConfig() error {
	statePath, err := config.DefaultStatePath()
	if err != nil {
		return err
	}
	a.statePath = statePath

	s, err := config.LoadState(statePath)
	if err != nil {
		return err
	}
	a.state = s

	cfgPath := strings.TrimSpace(a.opts.Config)
	if cfgPath == "" {
		p, err := config.DefaultConfigPath()
		if err == nil {
			cfgPath = p
		}
	}

	if cfgPath != "" {
		cfg, source, err := config.LoadFile(cfgPath)
		if err != nil {
			return err
		}
		a.cfgFile = cfg
		a.configPath = cfgPath
		if source != "" {
			a.configSource = source
		}
	}
	if a.configPath == "" {
		if p, err := config.DefaultConfigPath(); err == nil {
			a.configPath = p
		}
	}
	if a.configSource == "" {
		a.configSource = "none"
	}

	width := 120
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		width = w
	}
	noColor := strings.TrimSpace(os.Getenv("NO_COLOR")) != ""
	a.bootHeader = logo.BootHeader(width, a.opts.Version, a.opts.Commit, a.configSource, noColor)
	return nil
}

// getOrCreateProfile fetches or creates a new Bifrost CLI profile
func (a *App) getOrCreateProfile() *config.Profile {
	if !a.opts.NoResume && strings.TrimSpace(a.state.LastProfileID) != "" {
		if p := a.state.ProfileByID(a.state.LastProfileID); p != nil {
			return p
		}
	}
	if len(a.state.Profiles) > 0 && !a.opts.NoResume {
		return &a.state.Profiles[0]
	}

	p := config.Profile{ID: "default", Name: "Default"}
	if a.cfgFile != nil {
		p.BaseURL = strings.TrimSpace(a.cfgFile.BaseURL)
	}

	if existing := a.state.ProfileByID("default"); existing != nil {
		if strings.TrimSpace(existing.BaseURL) == "" {
			existing.BaseURL = p.BaseURL
		}
		return existing
	}
	a.state.Profiles = append(a.state.Profiles, p)
	return &a.state.Profiles[len(a.state.Profiles)-1]
}

// harnessOptions responds with available harness options with states like installed/not installed etc.
// Version detection runs concurrently across all harnesses to avoid serial subprocess waits.
func (a *App) harnessOptions() []tui.HarnessOption {
	ids := harness.IDs()
	out := make([]tui.HarnessOption, len(ids))

	var wg sync.WaitGroup
	for i, id := range ids {
		h, _ := harness.Get(id)
		out[i] = tui.HarnessOption{
			ID:                    h.ID,
			Label:                 h.Label,
			SupportsWorktree:      h.SupportsWorktree,
			SupportsModelOverride: h.RunArgsForMod != nil || h.ModelEnv != "" || h.PreLaunch != nil,
		}
		wg.Add(1)
		go func(idx int, h harness.Harness) {
			defer wg.Done()
			out[idx].Installed = installer.IsInstalled(h)
			out[idx].Version = harness.DetectVersion(h)
		}(i, h)
	}
	wg.Wait()
	return out
}
