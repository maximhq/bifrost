package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maximhq/bifrost/cli/internal/apis"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
	"github.com/maximhq/bifrost/cli/internal/installer"
	"github.com/maximhq/bifrost/cli/internal/mcp"
	"github.com/maximhq/bifrost/cli/internal/runtime"
	"github.com/maximhq/bifrost/cli/internal/secrets"
	"github.com/maximhq/bifrost/cli/internal/ui/logo"
	"github.com/maximhq/bifrost/cli/internal/ui/tui"
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

// Run starts the interactive TUI loop. It loads config and state, then repeatedly
// presents the chooser and launches the selected harness until the user quits.
func (a *App) Run(ctx context.Context) error {
	if err := a.loadStateAndConfig(); err != nil {
		return err
	}

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
	message := ""
	afterSession := false
	var rawState *term.State
	for {
		harnesses := a.harnessOptions()
		if afterSession {
			if rawState != nil {
				term.Restore(int(os.Stdin.Fd()), rawState)
				rawState = nil
			}
			signal.Reset(syscall.SIGINT)
		}
		choice, err := tui.RunChooser(tui.ChooserConfig{
			Version:      a.opts.Version,
			Commit:       a.opts.Commit,
			ConfigSrc:    a.configSource,
			Message:      message,
			BaseURL:      activeProfile.BaseURL,
			VirtualKey:   vk,
			Harness:      selection.Harness,
			Model:        selection.Model,
			Worktree:     worktree,
			AfterSession: afterSession,
			Harnesses:   harnesses,
			FetchModels: a.apiClient.ListModels,
		})
		if err != nil {
			return err
		}
		if choice.Quit {
			return nil
		}

		activeProfile.BaseURL = strings.TrimSpace(choice.BaseURL)
		selection.Harness = strings.TrimSpace(choice.Harness)
		selection.Model = strings.TrimSpace(choice.Model)
		vk = strings.TrimSpace(choice.VirtualKey)
		worktree = strings.TrimSpace(choice.Worktree)

		h, ok := harness.Get(selection.Harness)
		if !ok {
			message = "invalid harness selected"
			continue
		}

		// Handle install request — chooser exits early when user picks an uninstalled harness
		if choice.InstallHarness {
			cmd, args := installer.InstallCommand(h)
			shouldInstall, err := tui.RunConfirmInstall(a.bootHeader, h.Label, cmd+" "+strings.Join(args, " "))
			if err != nil {
				return err
			}
			if !shouldInstall {
				message = h.Label + " installation skipped"
				continue
			}
			if err := installer.EnsureNPM(); err != nil {
				message = err.Error()
				continue
			}
			fmt.Fprintf(a.out, "\nInstalling %s...\n", h.Label)
			if err := installer.RunInstall(ctx, a.out, a.errOut, h); err != nil {
				message = err.Error()
				continue
			}
			if !installer.IsInstalled(h) {
				message = h.Label + " installed but binary still not in PATH"
				continue
			}
			message = h.Label + " installed successfully"
			continue // re-enter chooser so user can proceed to model selection
		}

		if err := secrets.SetVirtualKey(activeProfile.ID, vk); err != nil {
			fmt.Fprintf(a.errOut, "warning: %v\n", err)
		}

		a.state.LastProfileID = activeProfile.ID
		a.state.Selections[activeProfile.ID] = selection
		if err := config.SaveState(a.statePath, a.state); err != nil {
			fmt.Fprintf(a.errOut, "warning: %v\n", err)
		}

		// Persist selections to config file
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

		err = runtime.RunInteractive(ctx, a.out, a.errOut, runtime.LaunchSpec{
			Harness:    h,
			BaseURL:    activeProfile.BaseURL,
			VirtualKey: vk,
			Model:      selection.Model,
			Worktree:   worktree,
		})
		afterSession = true
		signal.Ignore(syscall.SIGINT)
		rawState, _ = term.MakeRaw(int(os.Stdin.Fd()))
		ts := time.Now().Format("15:04:05")
		if err != nil {
			message = fmt.Sprintf("[%s] harness exited with error: %v", ts, err)
		} else {
			message = fmt.Sprintf("[%s] harness exited", ts)
		}
	}
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

// harnessOptions responds with available harness options with states like installed/not installed etc
func (a *App) harnessOptions() []tui.HarnessOption {
	ids := harness.IDs()
	out := make([]tui.HarnessOption, 0, len(ids))
	for _, id := range ids {
		h, _ := harness.Get(id)
		out = append(out, tui.HarnessOption{
			ID:                    h.ID,
			Label:                 h.Label,
			Version:               harness.DetectVersion(h),
			Installed:             installer.IsInstalled(h),
			SupportsWorktree:      h.SupportsWorktree,
			SupportsModelOverride: h.RunArgsForMod != nil || h.ModelEnv != "" || h.PreLaunch != nil,
		})
	}
	return out
}
