package command

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/output"
	"github.com/maximhq/bifrost/cli/internal/secrets"
	"golang.org/x/term"
)

const agentDeviceProfileID = "installation"

// runAuth manages credentials for the selected gateway context.
func (r *Runner) runAuth(ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, `Usage: bifrost auth <status|login|logout|whoami|print-token|select-virtual-key|clear-selection|set-virtual-key|set-management-key|clear-virtual-key|clear-management-key>

"bifrost auth login" uses Enterprise SSO in your browser.
Use --username for the OSS dashboard password flow.
`)
		return err
	}
	switch args[0] {
	case "status":
		return r.authStatus(ctx, env)
	case "login":
		return r.authLogin(ctx, env, args[1:])
	case "logout":
		return r.authLogout(ctx, env)
	case "whoami":
		return r.authWhoAmI(ctx, env)
	case "print-token":
		return r.authPrintToken(ctx, env, args[1:])
	case "select-virtual-key":
		return r.selectAgentVirtualKey(ctx, env, args[1:])
	case "clear-selection":
		return r.clearAgentVirtualKeySelection(env)
	case "set-virtual-key":
		return r.setCredential(env, secrets.VirtualKey, args[1:])
	case "set-management-key":
		return r.setCredential(env, secrets.ManagementKey, args[1:])
	case "clear-virtual-key":
		return r.clearCredential(env, secrets.VirtualKey)
	case "clear-management-key":
		return r.clearCredential(env, secrets.ManagementKey)
	default:
		return fmt.Errorf("unknown auth action %q", args[0])
	}
}

// authPrintToken emits a currently valid Enterprise agent credential for
// coding-agent token helpers. Nothing except the token is written to stdout.
func (r *Runner) authPrintToken(ctx context.Context, env *environment, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: bifrost auth print-token")
	}
	if strings.TrimSpace(env.Client.CredentialsSnapshot().AgentToken) == "" {
		return fmt.Errorf("no Enterprise SSO session is available; run 'bifrost auth login'")
	}
	if _, err := env.Client.Do(ctx, client.Request{Path: "/v1/models", Auth: client.AuthInference}); err != nil {
		return fmt.Errorf("validate Enterprise SSO session: %w", err)
	}
	token := strings.TrimSpace(env.Client.CredentialsSnapshot().AgentToken)
	if token == "" {
		return fmt.Errorf("enterprise SSO session returned an empty agent token")
	}
	_, err := fmt.Fprintln(r.Out, token)
	return err
}

// authStatus combines local credential presence with the server auth status.
func (r *Runner) authStatus(ctx context.Context, env *environment) error {
	credentials := env.Client.CredentialsSnapshot()
	result := map[string]any{
		"context":                    env.ProfileID,
		"has_virtual_key":            strings.TrimSpace(credentials.VirtualKey) != "",
		"has_management_key":         strings.TrimSpace(credentials.ManagementKey) != "",
		"has_stored_session_token":   strings.TrimSpace(credentials.SessionToken) != "",
		"has_enterprise_sso_session": strings.TrimSpace(credentials.AgentToken) != "",
		"has_agent_refresh_token":    false,
		"agent_virtual_key_id":       credentials.AgentVirtualKeyID,
		"inference_credential_type":  inferenceCredentialType(credentials),
		"management_credential_type": managementCredentialType(credentials),
	}
	if refreshToken, refreshErr := r.resolveSecret(env.Profile, env.ProfileID, secrets.AgentRefreshToken, "BIFROST_AGENT_REFRESH_TOKEN"); refreshErr == nil {
		result["has_agent_refresh_token"] = strings.TrimSpace(refreshToken) != ""
	} else {
		result["refresh_token_error"] = refreshErr.Error()
	}
	if env.BrowserAuth != nil {
		if status, statusErr := env.BrowserAuth.CheckStatus(ctx); statusErr == nil {
			result["browser_sso"] = status
		} else {
			result["browser_sso"] = map[string]any{"available": false, "error": statusErr.Error()}
		}
	}
	response, err := env.Client.Do(ctx, client.Request{Path: "/api/session/is-auth-enabled", Auth: client.AuthManagement})
	if err != nil {
		result["server"] = map[string]any{"reachable": false, "error": err.Error()}
	} else {
		var server any
		if json.Unmarshal(response.Body, &server) != nil {
			server = strings.TrimSpace(string(response.Body))
		}
		result["server"] = server
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return marshalErr
	}
	if env.Quiet {
		return nil
	}
	return output.Print(r.Out, body, env.Output)
}

// inferenceCredentialType reports the effective inference credential precedence.
func inferenceCredentialType(credentials client.Credentials) string {
	if strings.TrimSpace(credentials.AgentToken) != "" {
		return "enterprise-sso"
	}
	if strings.TrimSpace(credentials.VirtualKey) != "" {
		return "virtual-key"
	}
	return "none"
}

// managementCredentialType reports the credential precedence without exposing a secret.
func managementCredentialType(credentials client.Credentials) string {
	if strings.TrimSpace(credentials.ManagementKey) != "" {
		return "management-api-key"
	}
	if strings.TrimSpace(credentials.SessionToken) != "" {
		return "session-token"
	}
	return "none"
}

// authLogin uses Enterprise browser SSO unless the OSS username flow is requested.
func (r *Runner) authLogin(ctx context.Context, env *environment, args []string) error {
	if err := requireNamedAuthContext(env); err != nil {
		return err
	}
	fs := flag.NewFlagSet("bifrost auth login", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var username string
	var passwordStdin bool
	var noBrowser bool
	fs.StringVar(&username, "username", "", "dashboard admin username")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	fs.BoolVar(&noBrowser, "no-browser", false, "print the sign-in URL without opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(username) == "" {
		return r.authBrowserLogin(ctx, env, noBrowser)
	}
	return r.authPasswordLogin(ctx, env, username, passwordStdin)
}

// authBrowserLogin completes Enterprise SSO and stores only opaque Bifrost credentials.
func (r *Runner) authBrowserLogin(ctx context.Context, env *environment, noBrowser bool) error {
	if env.BrowserAuth == nil {
		return fmt.Errorf("browser authentication is unavailable")
	}
	env.BrowserAuth.Authorization = func(target string) {
		if noBrowser {
			fmt.Fprintln(r.ErrOut, "Open this URL to sign in to Bifrost:")
		} else {
			fmt.Fprintln(r.ErrOut, "Opening your browser to sign in to Bifrost...")
			fmt.Fprintln(r.ErrOut, "If the browser does not open, use this URL:")
		}
		fmt.Fprintln(r.ErrOut, target)
		fmt.Fprintln(r.ErrOut, "Waiting for authentication...")
	}
	env.BrowserAuth.Warning = func(err error) { fmt.Fprintf(r.ErrOut, "Warning: %v\n", err) }
	deviceID, err := r.ensureAgentDeviceID()
	if err != nil {
		return err
	}
	env.BrowserAuth.HardwareID = deviceID
	response, err := env.BrowserAuth.SignIn(ctx, noBrowser)
	if err != nil {
		return err
	}
	// Store the rotated refresh token first. If the second keyring write fails,
	// the next invocation can still recover instead of retaining a consumed token.
	if err := r.Secrets.Set(env.ProfileID, secrets.AgentRefreshToken, response.RefreshToken); err != nil {
		return err
	}
	if err := r.Secrets.Set(env.ProfileID, secrets.AgentToken, response.AccessToken); err != nil {
		return err
	}
	userJSON, err := json.Marshal(response.User)
	if err != nil {
		return err
	}
	if err := r.Secrets.Set(env.ProfileID, secrets.AgentUser, string(userJSON)); err != nil {
		return err
	}
	if err := r.Secrets.Delete(env.ProfileID, secrets.AgentVirtualKeyID); err != nil {
		return err
	}
	env.Client.SetAgentToken(response.AccessToken)
	env.Client.SetAgentVirtualKeyID("")
	if !env.Quiet {
		identity := strings.TrimSpace(response.User.Email)
		if identity == "" {
			identity = strings.TrimSpace(response.User.Name)
		}
		if identity == "" {
			identity = response.User.ID
		}
		if _, err := fmt.Fprintf(r.Out, "Signed in to %s as %s using context %q.\n", env.Client.BaseURL, identity, env.ProfileID); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(r.Out, "Enterprise SSO now authorizes user-scoped inference. Management commands still require a management API key."); err != nil {
			return err
		}
		if env.AgentAPI != nil {
			if keys, listErr := env.AgentAPI.ListVirtualKeys(ctx); listErr == nil {
				if _, err := fmt.Fprintf(r.Out, "%d assigned virtual key(s) available", len(keys.VirtualKeys)); err != nil {
					return err
				}
				defaultLabel := ""
				for _, key := range keys.VirtualKeys {
					if key.ID == keys.SelectedVirtualKeyID {
						defaultLabel = assignedVirtualKeyLabel(key.Name, key.ID)
						break
					}
				}
				if defaultLabel != "" {
					if _, err := fmt.Fprintf(r.Out, "; default: %s", defaultLabel); err != nil {
						return err
					}
				}
				if _, err := fmt.Fprintln(r.Out, "."); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintf(r.ErrOut, "Warning: signed in, but assigned virtual keys could not be loaded: %v\n", listErr); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureAgentDeviceID returns a stable opaque installation ID without reading a machine identifier.
func (r *Runner) ensureAgentDeviceID() (string, error) {
	value, err := r.Secrets.Get(agentDeviceProfileID, secrets.AgentDeviceID)
	if err != nil {
		return "", err
	}
	if value = strings.TrimSpace(value); value != "" {
		return value, nil
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create opaque CLI device ID: %w", err)
	}
	value = "cli-" + base64.RawURLEncoding.EncodeToString(random)
	if err := r.Secrets.Set(agentDeviceProfileID, secrets.AgentDeviceID, value); err != nil {
		return "", err
	}
	return value, nil
}

// assignedVirtualKeyLabel returns a readable name and ID without key material.
func assignedVirtualKeyLabel(name, id string) string {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if name == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", name, id)
}

// authPasswordLogin exchanges an OSS admin password for a dashboard session token.
func (r *Runner) authPasswordLogin(ctx context.Context, env *environment, username string, passwordStdin bool) error {
	password, err := r.readSecret("Password: ", passwordStdin)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"username": strings.TrimSpace(username), "password": password})
	if err != nil {
		return err
	}
	response, err := env.Client.Do(ctx, client.Request{Method: http.MethodPost, Path: "/api/session/login", Body: payload, Auth: client.AuthNone})
	if err != nil {
		return err
	}
	token := sessionToken(response)
	if token == "" {
		var parsed struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(response.Body, &parsed)
		token = strings.TrimSpace(parsed.Token)
	}
	if token == "" {
		return fmt.Errorf("gateway accepted login but returned no session token cookie")
	}
	if err := r.Secrets.Set(env.ProfileID, secrets.SessionToken, token); err != nil {
		return err
	}
	env.Client.SetSessionToken(token)
	if !env.Quiet {
		if _, err := fmt.Fprintf(r.Out, "Logged in to %s using context %q.\n", env.Client.BaseURL, env.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

// sessionToken extracts the dashboard token from a login Set-Cookie header.
func sessionToken(response *client.Response) string {
	httpResponse := &http.Response{Header: response.Header}
	for _, cookie := range httpResponse.Cookies() {
		if cookie.Name == "token" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	return ""
}

// authWhoAmI reports either the management identity or the stored Enterprise SSO identity.
func (r *Runner) authWhoAmI(ctx context.Context, env *environment) error {
	credentials := env.Client.CredentialsSnapshot()
	if strings.TrimSpace(credentials.ManagementKey) != "" || strings.TrimSpace(credentials.SessionToken) != "" {
		return r.executeAndPrint(ctx, env, client.Request{Path: "/api/governance/users/me/permissions", Auth: client.AuthManagement})
	}
	if strings.TrimSpace(credentials.AgentToken) == "" {
		return fmt.Errorf("not signed in; use 'bifrost auth login' or configure a management API key")
	}
	if _, err := env.Client.Do(ctx, client.Request{Path: "/api/agent/virtual-keys", Auth: client.AuthAgent}); err != nil {
		return err
	}
	identity, err := r.Secrets.Get(env.ProfileID, secrets.AgentUser)
	if err != nil {
		return err
	}
	if strings.TrimSpace(identity) == "" {
		return fmt.Errorf("the SSO identity is unavailable; sign in again with 'bifrost auth login'")
	}
	if env.Quiet {
		return nil
	}
	return output.Print(r.Out, []byte(identity), env.Output)
}

// selectAgentVirtualKey validates and stores an assigned-key ID for SSO inference.
func (r *Runner) selectAgentVirtualKey(ctx context.Context, env *environment, args []string) error {
	if err := requireNamedAuthContext(env); err != nil {
		return err
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: bifrost auth select-virtual-key <id>")
	}
	if strings.TrimSpace(env.Client.CredentialsSnapshot().AgentToken) == "" {
		return fmt.Errorf("enterprise SSO is required; run 'bifrost auth login'")
	}
	if env.AgentAPI == nil {
		return fmt.Errorf("agent API client is unavailable")
	}
	requestedID := strings.TrimSpace(args[0])
	keys, err := env.AgentAPI.ListVirtualKeys(ctx)
	if err != nil {
		return err
	}
	if forcedID := strings.TrimSpace(keys.ForcedVirtualKeyID); forcedID != "" && requestedID != forcedID {
		return fmt.Errorf("virtual key %q cannot be selected because your administrator requires %q", requestedID, forcedID)
	}
	selectedLabel := ""
	for _, key := range keys.VirtualKeys {
		if key.ID == requestedID && key.IsActive {
			selectedLabel = assignedVirtualKeyLabel(key.Name, key.ID)
			break
		}
	}
	if selectedLabel == "" {
		return fmt.Errorf("virtual key %q is not active and assigned to the signed-in user", requestedID)
	}
	if err := r.Secrets.Set(env.ProfileID, secrets.AgentVirtualKeyID, requestedID); err != nil {
		return err
	}
	env.Client.SetAgentVirtualKeyID(requestedID)
	if !env.Quiet {
		if _, err := fmt.Fprintf(r.Out, "Selected %s for Enterprise SSO inference in context %q.\n", selectedLabel, env.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

// clearAgentVirtualKeySelection returns SSO inference to the gateway's deterministic default.
func (r *Runner) clearAgentVirtualKeySelection(env *environment) error {
	if err := r.Secrets.Delete(env.ProfileID, secrets.AgentVirtualKeyID); err != nil {
		return err
	}
	env.Client.SetAgentVirtualKeyID("")
	if !env.Quiet {
		if _, err := fmt.Fprintf(r.Out, "Cleared the Enterprise SSO virtual-key selection for context %q.\n", env.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

// authLogout invalidates and removes the selected context's user sessions.
func (r *Runner) authLogout(ctx context.Context, env *environment) error {
	var requestErr error
	credentials := env.Client.CredentialsSnapshot()
	agentToken := strings.TrimSpace(credentials.AgentToken)
	if agentToken != "" && env.BrowserAuth != nil {
		requestErr = env.BrowserAuth.Logout(ctx, agentToken)
	}
	if strings.TrimSpace(credentials.SessionToken) != "" {
		_, sessionErr := env.Client.Do(ctx, client.Request{Method: http.MethodPost, Path: "/api/session/logout", Auth: client.AuthManagement})
		if requestErr == nil {
			requestErr = sessionErr
		}
	}
	for _, kind := range []secrets.Kind{secrets.SessionToken, secrets.AgentToken, secrets.AgentRefreshToken, secrets.AgentUser, secrets.AgentVirtualKeyID} {
		if deleteErr := r.Secrets.Delete(env.ProfileID, kind); deleteErr != nil {
			return deleteErr
		}
	}
	if requestErr != nil {
		return fmt.Errorf("local session removed; gateway logout failed: %w", requestErr)
	}
	if !env.Quiet {
		if _, err := fmt.Fprintln(r.Out, "Logged out and removed the stored session credentials."); err != nil {
			return err
		}
	}
	return nil
}

// setCredential reads and stores one secret without writing it to normal config files.
func (r *Runner) setCredential(env *environment, kind secrets.Kind, args []string) error {
	if err := requireNamedAuthContext(env); err != nil {
		return err
	}
	fs := flag.NewFlagSet("bifrost auth set-credential", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var stdin bool
	fs.BoolVar(&stdin, "stdin", false, "read the credential from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	value, err := r.readSecret("Credential: ", stdin)
	if err != nil {
		return err
	}
	if err := r.Secrets.Set(env.ProfileID, kind, value); err != nil {
		return err
	}
	if !env.Quiet {
		if _, err := fmt.Fprintf(r.Out, "Stored %s for context %q.\n", kind, env.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

func requireNamedAuthContext(env *environment) error {
	if env == nil || env.Profile == nil {
		return fmt.Errorf("persistent authentication requires a named context; run 'bifrost context add <name> --base-url <url>' first")
	}
	return nil
}

// clearCredential removes one secret from the selected context.
func (r *Runner) clearCredential(env *environment, kind secrets.Kind) error {
	if err := r.Secrets.Delete(env.ProfileID, kind); err != nil {
		return err
	}
	if !env.Quiet {
		if _, err := fmt.Fprintf(r.Out, "Removed %s from context %q.\n", kind, env.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

// readSecret reads a line from stdin or hides terminal input when possible.
// The exact bytes the user provided are preserved (only the line terminator
// is stripped), since a gateway may hash and compare a credential without
// trimming it — a value with intentional leading or trailing whitespace must
// not be silently corrupted.
func (r *Runner) readSecret(prompt string, fromStdin bool) (string, error) {
	if fromStdin {
		reader := bufio.NewReader(r.In)
		value, err := reader.ReadString('\n')
		if err != nil && len(value) == 0 {
			return "", fmt.Errorf("read credential from stdin: %w", err)
		}
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
		if value == "" {
			return "", fmt.Errorf("credential cannot be empty")
		}
		return value, nil
	}
	file, ok := r.In.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("stdin is not a terminal; use --stdin or --password-stdin")
	}
	if _, err := fmt.Fprint(r.ErrOut, prompt); err != nil {
		return "", err
	}
	value, err := term.ReadPassword(int(file.Fd()))
	if _, printErr := fmt.Fprintln(r.ErrOut); printErr != nil {
		return "", printErr
	}
	if err != nil {
		return "", fmt.Errorf("read credential: %w", err)
	}
	secret := string(value)
	if secret == "" {
		return "", fmt.Errorf("credential cannot be empty")
	}
	return secret, nil
}
