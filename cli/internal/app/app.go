package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/cli/internal/apis"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
	"github.com/maximhq/bifrost/cli/internal/installer"
	"github.com/maximhq/bifrost/cli/internal/mcp"
	"github.com/maximhq/bifrost/cli/internal/runtime"
	"github.com/maximhq/bifrost/cli/internal/secrets"
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
	Config   string
	Worktree string
}

// App is the main Bifrost CLI application. It manages configuration, state,
// and the interactive TUI loop for selecting and launching harnesses.
type App struct {
	in        io.Reader
	out       io.Writer
	errOut    io.Writer
	opts      Options
	apiClient *apis.Client
	state     *config.State
	cfgFile   *config.FileConfig

	statePath    string
	configPath   string
	configSource string
	bootHeader   string
}

// New creates a new App instance with the given I/O streams and options.
func New(in io.Reader, out, errOut io.Writer, opts Options) *App {
	return &App{
		in:        in,
		out:       out,
		errOut:    errOut,
		opts:      opts,
		apiClient: apis.NewClient(),
	}
}

// Run starts the interactive TUI loop. It loads config and state, then presents
// the chooser, launches harnesses in a tabbed multiplexer, and loops back when
// all tabs are closed.
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

	// chooseAndPrepare runs the chooser TUI, handles installation flows,
	// persists state, and returns a launch spec. Loops internally until
	// the user picks a valid harness or quits.
	chooseAndPrepare := func(_ context.Context, notify func(runtime.TabNoticeLevel, string), stdinReader io.Reader, msg string, isAfterSession bool) (*runtime.LaunchSpec, error) {
		for {
			harnesses := a.harnessOptions()

			choice, err := tui.RunChooser(tui.ChooserConfig{
				Version:      a.opts.Version,
				Commit:       a.opts.Commit,
				ConfigSrc:    a.configSource,
				Message:      msg,
				BaseURL:      activeProfile.BaseURL,
				VirtualKey:   vk,
				Harness:      selection.Harness,
				Model:        selection.Model,
				Worktree:     worktree,
				AfterSession: isAfterSession,
				ReservedRows: 1, // bottom tab bar
				Harnesses:    harnesses,
				FetchModels:  a.apiClient.ListModels,
				Input:        stdinReader,
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
			if choice.Quit {
				return nil, nil
			}

			activeProfile.BaseURL = strings.TrimSpace(choice.BaseURL)
			selection.Harness = strings.TrimSpace(choice.Harness)
			selection.Model = strings.TrimSpace(choice.Model)
			vk = strings.TrimSpace(choice.VirtualKey)
			worktree = strings.TrimSpace(choice.Worktree)

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

			// Save virtual key
			if err := secrets.SetVirtualKey(activeProfile.ID, vk); err != nil {
				fmt.Fprintf(a.errOut, "warning: %v\n", err)
			}

			// Persist state
			a.state.LastProfileID = activeProfile.ID
			a.state.Selections[activeProfile.ID] = selection
			if err := config.SaveState(a.statePath, a.state); err != nil {
				fmt.Fprintf(a.errOut, "warning: %v\n", err)
			}

			// Persist config
			if a.cfgFile == nil {
				a.cfgFile = &config.FileConfig{}
			}
			a.cfgFile.BaseURL = activeProfile.BaseURL
			a.cfgFile.DefaultHarness = selection.Harness
			a.cfgFile.DefaultModel = selection.Model
			if a.configPath != "" {
				if err := config.SaveConfig(a.configPath, a.cfgFile); err != nil {
					fmt.Fprintf(a.errOut, "warning: save config: %v\n", err)
				}
			}

			mcp.AttachBestEffort(ctx, a.out, a.errOut, h, activeProfile.BaseURL, vk)

			return &runtime.LaunchSpec{
				Harness:    h,
				BaseURL:    activeProfile.BaseURL,
				VirtualKey: vk,
				Model:      selection.Model,
				Worktree:   worktree,
			}, nil
		}
	}

	// Main loop — each iteration enters tabbed mode (Home → chooser → tabs).
	// When all tabs close, we loop back.
	message := ""
	afterSession := false

	// Check for update result (non-blocking)
	select {
	case result := <-updateCh:
		if result != nil && result.UpdateAvailable {
			message = fmt.Sprintf("bifrost %s available — run 'bifrost update'", result.LatestVersion)
			a.state.LastVersionCheck = result.CheckedAt
			a.state.LastKnownVersion = result.LatestVersion
			_ = config.SaveState(a.statePath, a.state) // best-effort
		}
		updateCh = nil
	default:
	}

	// Enter tabbed mode — draws chrome, opens chooser, runs tabs.
	err = runtime.RunTabbed(ctx, a.out, a.errOut, a.opts.Version, func(tabCtx context.Context, notify func(runtime.TabNoticeLevel, string), stdinReader io.Reader) (*runtime.LaunchSpec, error) {
		return chooseAndPrepare(tabCtx, notify, stdinReader, message, afterSession)
	})

	if errors.Is(err, runtime.ErrQuit) {
		return nil
	}
	return err
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
