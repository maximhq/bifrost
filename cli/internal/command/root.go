// Package command implements the non-interactive Bifrost command tree.
package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/maximhq/bifrost/cli/internal/agentapi"
	"github.com/maximhq/bifrost/cli/internal/app"
	"github.com/maximhq/bifrost/cli/internal/browserauth"
	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
	"github.com/maximhq/bifrost/cli/internal/operations"
	"github.com/maximhq/bifrost/cli/internal/output"
	"github.com/maximhq/bifrost/cli/internal/secrets"
	"github.com/maximhq/bifrost/cli/internal/sessionauth"
	"github.com/maximhq/bifrost/cli/internal/update"
)

// BuildInfo contains version metadata injected while building the CLI.
type BuildInfo struct {
	Version string
	Commit  string
}

const (
	ExitSuccess       = 0
	ExitGeneral       = 1
	ExitAuthorization = 3
	ExitNotFound      = 4
	ExitConflict      = 5
	ExitUnavailable   = 6
)

// ExitCode maps common gateway failures to stable process exit codes.
func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ExitAuthorization
		case http.StatusNotFound:
			return ExitNotFound
		case http.StatusConflict:
			return ExitConflict
		default:
			if apiErr.StatusCode >= http.StatusInternalServerError {
				return ExitUnavailable
			}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitUnavailable
	}
	return ExitGeneral
}

// SecretStore abstracts keyring access for commands and tests.
type SecretStore interface {
	Get(profileID string, kind secrets.Kind) (string, error)
	Set(profileID string, kind secrets.Kind, value string) error
	Delete(profileID string, kind secrets.Kind) error
}

// Runner owns the command dependencies and input/output streams.
type Runner struct {
	In         io.Reader
	Out        io.Writer
	ErrOut     io.Writer
	Build      BuildInfo
	Secrets    SecretStore
	HTTPClient *http.Client
	StatePath  string
}

// globalOptions contains flags shared by all non-interactive commands.
type globalOptions struct {
	ContextName string
	BaseURL     string
	Output      string
	Timeout     time.Duration
	Debug       bool
	Quiet       bool
}

// environment contains the resolved gateway and authentication context.
type environment struct {
	State       *config.State
	StatePath   string
	Profile     *config.Profile
	Client      *client.Client
	Output      output.Format
	ProfileID   string
	VirtualKey  string
	Quiet       bool
	BrowserAuth *browserauth.Client
	AgentAPI    *agentapi.Client
}

type credentialRequirements uint8

const (
	credentialVirtualKey credentialRequirements = 1 << iota
	credentialManagementKey
	credentialSessionToken
	credentialAgentToken
	credentialAgentVirtualKeyID
	credentialsManagement = credentialManagementKey | credentialSessionToken
	credentialsInference  = credentialVirtualKey | credentialAgentToken | credentialAgentVirtualKeyID
	credentialsAll        = credentialsManagement | credentialsInference
)

type commandHandler func(*Runner, context.Context, *environment, []string) error

type commandSpec struct {
	offlineHelp bool
	credentials func([]string) credentialRequirements
	handler     commandHandler
}

func fixedCredentials(requirements credentialRequirements) func([]string) credentialRequirements {
	return func([]string) credentialRequirements { return requirements }
}

var commandSpecs = map[string]commandSpec{
	"auth": {
		offlineHelp: true, credentials: authCredentialRequirements,
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runAuth(ctx, env, args)
		},
	},
	"usage": {
		offlineHelp: true, credentials: fixedCredentials(credentialAgentToken | credentialAgentVirtualKeyID),
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runUsage(ctx, env, args)
		},
	},
	"config": {
		offlineHelp: true, credentials: fixedCredentials(0),
		handler: func(r *Runner, _ context.Context, env *environment, args []string) error {
			return r.runLocalConfig(env, args)
		},
	},
	"status": {
		offlineHelp: true, credentials: fixedCredentials(credentialsManagement),
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
				_, err := fmt.Fprintln(r.Out, "Usage: bifrost status")
				return err
			}
			if len(args) != 0 {
				return fmt.Errorf("unexpected status arguments: %s", strings.Join(args, " "))
			}
			return r.runStatus(ctx, env)
		},
	},
	"doctor": {
		offlineHelp: true, credentials: fixedCredentials(credentialsAll),
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
				_, err := fmt.Fprintln(r.Out, "Usage: bifrost doctor")
				return err
			}
			if len(args) != 0 {
				return fmt.Errorf("unexpected doctor arguments: %s", strings.Join(args, " "))
			}
			return r.runDoctor(ctx, env)
		},
	},
	"request": {
		offlineHelp: true, credentials: requestCredentialRequirements,
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runRequest(ctx, env, args)
		},
	},
	"http": {
		offlineHelp: true, credentials: httpCredentialRequirements,
		handler: runHTTPCommand,
	},
	"api": {
		credentials: apiCredentialRequirements,
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runAPI(ctx, env, args)
		},
	},
	"chat": {
		offlineHelp: true, credentials: fixedCredentials(credentialsInference),
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runChat(ctx, env, args)
		},
	},
	"infer": {
		offlineHelp: true, credentials: fixedCredentials(credentialsInference),
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runInfer(ctx, env, args)
		},
	},
	"run": {
		offlineHelp: true, credentials: fixedCredentials(credentialsInference),
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runHarness(ctx, env, args)
		},
	},
	"configure": {
		offlineHelp: true, credentials: fixedCredentials(credentialVirtualKey),
		handler: func(r *Runner, _ context.Context, env *environment, args []string) error {
			return r.configureHarness(env, args)
		},
	},
	"unconfigure": {
		offlineHelp: true, credentials: fixedCredentials(0),
		handler: func(r *Runner, _ context.Context, env *environment, args []string) error {
			return r.unconfigureHarness(env, args)
		},
	},
	"keys": {
		offlineHelp: true, credentials: func(args []string) credentialRequirements {
			return resourceCredentialRequirements("virtual-keys", args)
		},
		handler: func(r *Runner, ctx context.Context, env *environment, args []string) error {
			return r.runResource(ctx, env, "virtual-keys", args)
		},
	},
}

// New creates a command runner backed by the system keyring.
func New(in io.Reader, out, errOut io.Writer, build BuildInfo) *Runner {
	return &Runner{In: in, Out: out, ErrOut: errOut, Build: build, Secrets: secrets.Keyring{}}
}

// Run dispatches one invocation while preserving the legacy launcher behavior.
func (r *Runner) Run(ctx context.Context, args []string) error {
	if r.In == nil {
		r.In = os.Stdin
	}
	if r.Out == nil {
		r.Out = os.Stdout
	}
	if r.ErrOut == nil {
		r.ErrOut = os.Stderr
	}
	if r.Secrets == nil {
		r.Secrets = secrets.Keyring{}
	}
	if len(args) == 0 || isLegacyLauncherInvocation(args) {
		return r.runLauncher(ctx, args)
	}

	globals, remaining, err := r.parseGlobals(args)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return r.runLauncher(ctx, nil)
	}
	name := remaining[0]
	commandArgs := remaining[1:]
	switch name {
	case "help", "--help", "-h":
		return r.printHelp()
	case "version":
		_, err := fmt.Fprintf(r.Out, "bifrost %s (%s)\n", r.Build.Version, r.Build.Commit)
		return err
	case "update":
		return update.RunSelfUpdate(r.Build.Version)
	case "launch":
		return r.runLauncher(ctx, commandArgs)
	case "context":
		format, formatErr := output.ParseFormat(globals.Output)
		if formatErr != nil {
			return formatErr
		}
		return r.runContext(commandArgs, format)
	case "completion":
		return r.runCompletion(commandArgs)
	}
	if name == "api" && (len(commandArgs) == 0 || commandArgs[0] != "call") {
		format, formatErr := output.ParseFormat(globals.Output)
		if formatErr != nil {
			return formatErr
		}
		return r.runAPI(ctx, &environment{Output: format}, commandArgs)
	}
	if name == "resources" {
		format, formatErr := output.ParseFormat(globals.Output)
		if formatErr != nil {
			return formatErr
		}
		return r.runResources(format, commandArgs)
	}
	if supportsOfflineHelp(name) && isHelpRequest(commandArgs) && !bareInvocationRuns(name, commandArgs) {
		return r.runCommandHelp(ctx, name, commandArgs, globals.Output)
	}

	if spec, ok := commandSpecs[name]; ok {
		env, envErr := r.resolveEnvironment(globals, spec.credentials(commandArgs))
		if envErr != nil {
			return envErr
		}
		return spec.handler(r, ctx, env, commandArgs)
	}
	if _, ok := harness.Get(name); ok {
		env, envErr := r.resolveEnvironment(globals, credentialsInference)
		if envErr != nil {
			return envErr
		}
		return r.runHarness(ctx, env, append([]string{name}, normalizeHarnessAliasArgs(commandArgs)...))
	}
	if _, ok := resourceRegistry[name]; ok {
		env, envErr := r.resolveEnvironment(globals, resourceCredentialRequirements(name, commandArgs))
		if envErr != nil {
			return envErr
		}
		return r.runResource(ctx, env, name, commandArgs)
	}
	return fmt.Errorf("unknown command %q; run 'bifrost help'", name)
}

// normalizeHarnessAliasArgs separates Bifrost wrapper flags from agent-native
// arguments so direct aliases can forward unfamiliar flags without requiring --.
func normalizeHarnessAliasArgs(args []string) []string {
	prefix := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			return append(prefix, args[index:]...)
		}
		name := argument
		hasInlineValue := false
		if before, _, ok := strings.Cut(argument, "="); ok {
			name = before
			hasInlineValue = true
		}
		switch name {
		case "--skip-verify":
			prefix = append(prefix, argument)
		case "--model", "--worktree":
			prefix = append(prefix, argument)
			if !hasInlineValue {
				if index+1 >= len(args) {
					return prefix
				}
				index++
				prefix = append(prefix, args[index])
			}
		default:
			return append(append(prefix, "--"), args[index:]...)
		}
	}
	return prefix
}

// isHelpRequest reports whether a command invocation only asks for usage information.
func isHelpRequest(args []string) bool {
	return len(args) == 0 || args[0] == "--help" || args[0] == "-h"
}

// bareInvocationRuns reports whether a zero-argument invocation of name runs
// its primary action instead of showing offline help. "usage" defaults to
// its summary view, and a harness alias (claude, codex, ...) defaults to
// launching the agent — both are legitimate zero-argument invocations,
// unlike every other offline-help command where zero arguments means "show
// usage." An explicit --help/-h still shows help for either.
func bareInvocationRuns(name string, args []string) bool {
	if len(args) != 0 {
		return false
	}
	if name == "usage" || name == "status" || name == "doctor" {
		return true
	}
	_, ok := harness.Get(name)
	return ok
}

// supportsOfflineHelp reports whether a command has a help path independent of a gateway.
func supportsOfflineHelp(name string) bool {
	if _, ok := resourceRegistry[name]; ok {
		return true
	}
	if spec, ok := commandSpecs[name]; ok {
		return spec.offlineHelp
	}
	_, ok := harness.Get(name)
	return ok
}

// runCommandHelp renders command-specific help without requiring a configured gateway.
func (r *Runner) runCommandHelp(ctx context.Context, name string, args []string, formatValue string) error {
	format, err := output.ParseFormat(formatValue)
	if err != nil {
		return err
	}
	env := &environment{Output: format}
	if _, ok := resourceRegistry[name]; ok {
		return r.runResource(ctx, env, name, args)
	}
	if spec, ok := commandSpecs[name]; ok && spec.offlineHelp {
		return spec.handler(r, ctx, env, args)
	}
	if _, ok := harness.Get(name); ok {
		_, err := fmt.Fprintf(r.Out, "Usage: bifrost %s [--model MODEL] [--worktree NAME] [--skip-verify] [-- agent arguments]\n", name)
		return err
	}
	return fmt.Errorf("unknown command %q; run 'bifrost help'", name)
}

func runHTTPCommand(r *Runner, ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(r.Out, "Usage: bifrost http request <METHOD> <PATH> [flags]")
		return err
	}
	if args[0] != "request" {
		return fmt.Errorf("unknown http action %q; use 'bifrost http request'", args[0])
	}
	return r.runRequest(ctx, env, args[1:])
}

func authCredentialRequirements(args []string) credentialRequirements {
	if len(args) == 0 {
		return 0
	}
	switch args[0] {
	case "status":
		return credentialsAll
	case "logout":
		return credentialSessionToken | credentialAgentToken
	case "whoami":
		return credentialsManagement | credentialAgentToken
	case "print-token":
		return credentialAgentToken | credentialAgentVirtualKeyID
	case "select-virtual-key":
		return credentialAgentToken
	default:
		return 0
	}
}

func requestCredentialRequirements(args []string) credentialRequirements {
	if len(args) < 2 {
		return 0
	}
	mode := requestedAuthMode(client.ResolveAuthMode(client.AuthAuto, args[1]), args[2:])
	return credentialsForAuthMode(client.ResolveAuthMode(mode, args[1]))
}

func apiCredentialRequirements(args []string) credentialRequirements {
	if len(args) < 2 || args[0] != "call" {
		return 0
	}
	operation, ok := operations.Find(args[1])
	if !ok {
		return 0
	}
	mode := requestedAuthMode(client.ResolveAuthMode(client.AuthAuto, operation.Path), args[2:])
	return credentialsForAuthMode(client.ResolveAuthMode(mode, operation.Path))
}

func requestedAuthMode(fallback client.AuthMode, args []string) client.AuthMode {
	mode := fallback
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if strings.HasPrefix(argument, "--auth=") {
			parsed, err := parseAuthMode(strings.TrimPrefix(argument, "--auth="))
			if err == nil {
				mode = parsed
			}
			break
		}
		if argument == "--auth" && index+1 < len(args) {
			parsed, err := parseAuthMode(args[index+1])
			if err == nil {
				mode = parsed
			}
			break
		}
	}
	return mode
}

func httpCredentialRequirements(args []string) credentialRequirements {
	if len(args) == 0 || args[0] != "request" {
		return 0
	}
	return requestCredentialRequirements(args[1:])
}

func resourceCredentialRequirements(name string, args []string) credentialRequirements {
	if len(args) == 0 {
		return 0
	}
	descriptor, ok := resourceRegistry[name]
	if !ok {
		return 0
	}
	action, ok := descriptor.Actions[args[0]]
	if !ok {
		return 0
	}
	if name == "virtual-keys" && args[0] == "list" {
		return credentialsManagement | credentialAgentToken
	}
	return credentialsForAuthMode(client.ResolveAuthMode(action.Auth, action.Path))
}

func credentialsForAuthMode(mode client.AuthMode) credentialRequirements {
	switch mode {
	case client.AuthNone:
		return 0
	case client.AuthManagement:
		return credentialsManagement
	case client.AuthAgent:
		return credentialAgentToken
	case client.AuthInference:
		return credentialsInference
	default:
		return credentialsAll
	}
}

// isLegacyLauncherInvocation distinguishes historic launcher flags from global flags.
func isLegacyLauncherInvocation(args []string) bool {
	if len(args) == 0 {
		return true
	}
	name := args[0]
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "-config", "--config", "-no-resume", "--no-resume", "-worktree", "--worktree":
		return true
	default:
		return false
	}
}

// parseGlobals extracts shared flags from anywhere before a passthrough separator.
func (r *Runner) parseGlobals(args []string) (globalOptions, []string, error) {
	var options globalOptions
	globalArgs, remaining, err := splitGlobalArgs(args)
	if err != nil {
		return options, nil, err
	}
	fs := flag.NewFlagSet("bifrost", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	fs.StringVar(&options.ContextName, "context", strings.TrimSpace(os.Getenv("BIFROST_CONTEXT")), "named gateway context")
	fs.StringVar(&options.BaseURL, "base-url", strings.TrimSpace(os.Getenv("BIFROST_BASE_URL")), "gateway base URL")
	fs.StringVar(&options.Output, "output", environmentDefault("BIFROST_OUTPUT", "table"), "output format: table, json, yaml, raw")
	fs.DurationVar(&options.Timeout, "timeout", 30*time.Second, "request timeout")
	fs.BoolVar(&options.Debug, "debug", false, "print request diagnostics to stderr")
	fs.BoolVar(&options.Quiet, "quiet", false, "suppress successful response output")
	if err := fs.Parse(globalArgs); err != nil {
		return options, nil, err
	}
	return options, remaining, nil
}

// splitGlobalArgs removes known global flags while preserving command argument order.
func splitGlobalArgs(args []string) ([]string, []string, error) {
	valueFlags := map[string]bool{"--context": true, "--base-url": true, "--output": true, "--timeout": true}
	boolFlags := map[string]bool{"--debug": true, "--quiet": true}
	globalArgs := []string{}
	remaining := []string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if len(remaining) == 0 {
			if _, ok := harness.Get(argument); ok {
				remaining = append(remaining, args[index:]...)
				break
			}
		}
		if argument == "--" {
			remaining = append(remaining, args[index:]...)
			break
		}
		name := argument
		hasInlineValue := false
		if before, _, ok := strings.Cut(argument, "="); ok {
			name = before
			hasInlineValue = true
		}
		if boolFlags[name] {
			globalArgs = append(globalArgs, argument)
			continue
		}
		if valueFlags[name] && !commandOwnsGlobalFlag(remaining, name) {
			globalArgs = append(globalArgs, argument)
			if hasInlineValue {
				continue
			}
			if index+1 >= len(args) {
				return nil, nil, fmt.Errorf("flag %s requires a value", name)
			}
			index++
			globalArgs = append(globalArgs, args[index])
			continue
		}
		remaining = append(remaining, argument)
	}
	return globalArgs, remaining, nil
}

// commandOwnsGlobalFlag identifies flags that intentionally share a name with
// a global flag. Once context add's positional name has been seen, --base-url
// belongs to that command and must remain in its argument list.
func commandOwnsGlobalFlag(commandArgs []string, name string) bool {
	return name == "--base-url" &&
		len(commandArgs) >= 3 &&
		commandArgs[0] == "context" &&
		commandArgs[1] == "add"
}

// resolveEnvironment loads the selected profile and its independent credentials.
func (r *Runner) resolveEnvironment(options globalOptions, requirements credentialRequirements) (*environment, error) {
	format, err := output.ParseFormat(options.Output)
	if err != nil {
		return nil, err
	}
	statePath, err := r.resolveStatePath()
	if err != nil {
		return nil, err
	}
	state, err := config.LoadState(statePath)
	if err != nil {
		return nil, err
	}
	// --base-url without an explicit --context overrides only the
	// destination, not which profile's credentials to use. Loading the
	// implicit/default profile's credentials in that case would send them
	// to whatever host --base-url names, so treat the request as ad hoc
	// instead: no stored profile is selected, and resolveSecret below then
	// only honors an explicit environment-variable credential (CWE-200).
	adHocBaseURL := strings.TrimSpace(options.ContextName) == "" && strings.TrimSpace(options.BaseURL) != ""
	var profile *config.Profile
	if !adHocBaseURL {
		profile = selectProfile(state, options.ContextName)
		if strings.TrimSpace(options.ContextName) != "" && profile == nil {
			return nil, errorsForMissingContext(options.ContextName)
		}
	}
	profileID := "default"
	if profile != nil {
		profileID = profile.ID
	}
	baseURL := strings.TrimSpace(options.BaseURL)
	if baseURL == "" && profile != nil {
		baseURL = strings.TrimSpace(profile.BaseURL)
	}
	if baseURL == "" {
		if configPath, pathErr := config.DefaultConfigPath(); pathErr == nil {
			if fileConfig, _, loadErr := config.LoadFile(configPath); loadErr == nil && fileConfig != nil {
				baseURL = strings.TrimSpace(fileConfig.BaseURL)
			}
		}
	}
	if baseURL == "" {
		return nil, errors.New("no gateway URL configured; use 'bifrost context add <name> --base-url <url>' or --base-url")
	}

	var virtualKey, managementKey, sessionToken, agentToken, agentVirtualKeyID string
	load := func(required credentialRequirements, destination *string, kind secrets.Kind, environmentName string) error {
		if requirements&required == 0 {
			return nil
		}
		value, loadErr := r.resolveSecret(profile, profileID, kind, environmentName)
		if loadErr == nil {
			*destination = value
		}
		return loadErr
	}
	for _, item := range []struct {
		required    credentialRequirements
		destination *string
		kind        secrets.Kind
		environment string
	}{
		{credentialVirtualKey, &virtualKey, secrets.VirtualKey, "BIFROST_VIRTUAL_KEY"},
		{credentialManagementKey, &managementKey, secrets.ManagementKey, "BIFROST_MANAGEMENT_KEY"},
		{credentialSessionToken, &sessionToken, secrets.SessionToken, "BIFROST_SESSION_TOKEN"},
		{credentialAgentToken, &agentToken, secrets.AgentToken, "BIFROST_AGENT_TOKEN"},
		{credentialAgentVirtualKeyID, &agentVirtualKeyID, secrets.AgentVirtualKeyID, "BIFROST_AGENT_VIRTUAL_KEY_ID"},
	} {
		if err := load(item.required, item.destination, item.kind, item.environment); err != nil {
			return nil, err
		}
	}
	api := client.New(baseURL, client.Credentials{
		VirtualKey: virtualKey, ManagementKey: managementKey, SessionToken: sessionToken,
		AgentToken: agentToken, AgentVirtualKeyID: agentVirtualKeyID,
	}, options.Timeout)
	if r.HTTPClient != nil {
		api.HTTPClient = r.HTTPClient
	}
	api.UserAgent = "bifrost-cli/" + r.Build.Version
	browserClient := &browserauth.Client{BaseURL: baseURL, HTTPClient: api.HTTPClient, UserAgent: api.UserAgent, Version: r.Build.Version}
	if profile != nil {
		refresher := sessionauth.Refresher{
			Store: r.Secrets, ProfileID: profileID, StatePath: statePath, Client: browserClient,
		}
		api.RefreshAgentToken = refresher.Refresh
	}
	if options.Debug {
		api.DebugWriter = r.ErrOut
	}
	return &environment{
		State: state, StatePath: statePath, Profile: profile, Client: api, Output: format, ProfileID: profileID,
		VirtualKey: virtualKey, Quiet: options.Quiet, BrowserAuth: browserClient, AgentAPI: agentapi.New(api),
	}, nil
}

// resolveSecret applies environment-over-keyring precedence for one credential.
func (r *Runner) resolveSecret(profile *config.Profile, profileID string, kind secrets.Kind, environmentName string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(environmentName)); value != "" {
		return value, nil
	}
	if profile == nil {
		return "", nil
	}
	value, err := r.Secrets.Get(profileID, kind)
	if err != nil {
		return "", fmt.Errorf("load %s for context %q: %w", kind, profileID, err)
	}
	return value, nil
}

// resolveStatePath returns an explicit test path or the normal user state path.
func (r *Runner) resolveStatePath() (string, error) {
	if path := strings.TrimSpace(r.StatePath); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(os.Getenv("BIFROST_STATE_PATH")); path != "" {
		return path, nil
	}
	return config.DefaultStatePath()
}

// selectProfile chooses an explicit, current, or first available profile.
func selectProfile(state *config.State, name string) *config.Profile {
	if strings.TrimSpace(name) != "" {
		return state.ResolveProfile(strings.TrimSpace(name))
	}
	if state.LastProfileID != "" {
		if profile := state.ProfileByID(state.LastProfileID); profile != nil {
			return profile
		}
	}
	if len(state.Profiles) > 0 {
		return &state.Profiles[0]
	}
	return nil
}

// environmentDefault returns an environment value or a fallback.
func environmentDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// runLauncher parses the legacy launcher flags and opens the existing TUI.
func (r *Runner) runLauncher(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bifrost launch", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var configPath string
	var noResume bool
	var worktree string
	fs.StringVar(&configPath, "config", "", "path to config.json")
	fs.BoolVar(&noResume, "no-resume", false, "skip resume flow and open setup")
	fs.StringVar(&worktree, "worktree", "", "create a git worktree for the session")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected launcher arguments: %s", strings.Join(fs.Args(), " "))
	}
	application := app.New(r.In, r.Out, r.ErrOut, app.Options{
		Version: r.Build.Version, Commit: r.Build.Commit, NoResume: noResume, Config: configPath, Worktree: worktree,
	})
	return application.Run(ctx)
}

// printHelp writes the stable top-level command overview.
func (r *Runner) printHelp() error {
	_, err := fmt.Fprint(r.Out, `Bifrost CLI

Usage:
  bifrost [launcher flags]
  bifrost [global flags] <command> [arguments]

Global flags:
  --context NAME       Select a named gateway context
  --base-url URL       Override the gateway URL
  --output FORMAT      table, json, yaml, or raw
  --timeout DURATION   Request timeout; header timeout for streams (default 30s)
  --debug              Print request diagnostics
  --quiet              Suppress successful response output

Commands:
  launch               Open the coding-agent launcher
  run                  Run a coding agent directly
  claude, codex        Direct coding-agent aliases
  gemini, opencode     Direct coding-agent aliases
  configure            Persist reversible coding-agent configuration
  unconfigure          Restore coding-agent configuration from a receipt
  context              Manage gateway contexts
  config               Get or set local context defaults
  auth                 Manage context credentials and sessions
  usage                Show user-scoped budgets, rate limits, and usage
  status, doctor       Inspect gateway connectivity and authentication
  resources            Discover first-class resource commands
  models               List and inspect models
  providers            Manage providers
  credentials          Manage provider credentials
  virtual-keys         Manage inference virtual keys
  keys                 Short alias for virtual-keys
  management-keys      Manage Enterprise administrative API keys
  teams, customers     Manage governance entities
  users                Manage Enterprise users
  business-units       Manage Enterprise business units
  roles                Manage Enterprise RBAC roles
  model-configs        Manage governance model configurations
  pricing-overrides    Manage model pricing overrides
  routing-rules        Manage model routing rules
  prompts, skills      Manage reusable gateway content
  plugins, webhooks    Manage gateway extensions
  mcp-clients          Manage MCP gateway connections
  virtual-mcps         Manage virtual MCP servers
  alert-rules          Manage Enterprise alerting
  guardrails           Manage Enterprise guardrails
  devices, edge        Manage Enterprise agent fleet policy
  logs, mcp-logs       Query observability data
  chat                 Create a chat completion
  infer                Invoke a canonical inference endpoint
  request              Call any gateway-relative HTTP endpoint
  http request         Structured alias for request
  api                  Discover and call bundled OpenAPI operations
  update, version      Update or inspect this CLI
  completion           Generate shell completion

Exit codes:
  0 success; 1 general/usage error; 3 authentication/authorization
  4 not found; 5 conflict; 6 timeout or gateway/server unavailable

Run 'bifrost <command> --help' for command-specific help.
`)
	return err
}
