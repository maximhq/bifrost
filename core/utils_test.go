package bifrost

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type requestFallbackBenchmarkPlugin struct{ requestFallbackRoutingPlugin }

func (*requestFallbackBenchmarkPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: "cached", Model: "primary"}}}, nil
}

// Measures the complete core path without upstream latency, including routing and fallback setup.
func BenchmarkRequestFallbackDispatch(b *testing.B) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	client, err := Init(context.Background(), schemas.BifrostConfig{Account: account, Logger: NewNoOpLogger(), LLMPlugins: []schemas.LLMPlugin{&requestFallbackBenchmarkPlugin{}}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(client.Shutdown)
	for _, tc := range []struct {
		name      string
		fallbacks []schemas.Fallback
	}{
		{name: "none"},
		{name: "explicit", fallbacks: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "backup"}}},
		{name: "bare", fallbacks: []schemas.Fallback{{Model: "backup"}}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			req := &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "primary", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}}, Fallbacks: tc.fallbacks}
			b.ReportAllocs()
			for b.Loop() {
				req.Fallbacks = tc.fallbacks
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				if _, err := client.ChatCompletionRequest(ctx, req); err != nil {
					b.Fatal(err.GetErrorString())
				}
			}
		})
	}
}

func BenchmarkPrepareFallbackRequest(b *testing.B) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	client := &Bifrost{account: account, logger: NewNoOpLogger()}
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "primary"}}
	target := schemas.Fallback{Provider: schemas.OpenAI, Model: "backup"}
	b.ReportAllocs()
	for b.Loop() {
		if client.prepareFallbackRequest(req, target) == nil {
			b.Fatal("configured fallback was skipped")
		}
	}
}

func TestResolveFallbackProviders(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provider    schemas.ModelProvider
		input, want []schemas.Fallback
		cloned      bool
	}{
		{name: "absent", provider: schemas.OpenAI},
		{name: "empty SDK fallback stays invalid", provider: schemas.OpenAI, input: []schemas.Fallback{{}}, want: []schemas.Fallback{{}}},
		{name: "binding valid SDK shorthand does not activate empty entries", provider: schemas.OpenAI, input: []schemas.Fallback{{}, {Model: "backup"}}, want: []schemas.Fallback{{}, {Provider: schemas.OpenAI, Model: "backup"}}, cloned: true},
		{name: "unresolved primary is left for validation", input: []schemas.Fallback{{Model: "backup"}}, want: []schemas.Fallback{{Model: "backup"}}},
		{name: "explicit providers remain authoritative", provider: schemas.OpenAI, input: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "backup", KeyID: "key", Inferred: true}}, want: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "backup", KeyID: "key", Inferred: true}}},
		{name: "bare entries inherit without losing metadata", provider: schemas.OpenAI, input: []schemas.Fallback{{Model: "backup", KeyID: "key", Inferred: true}}, want: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "backup", KeyID: "key", Inferred: true}}, cloned: true},
		{name: "mixed chain never inherits the preceding fallback", provider: schemas.OpenAI, input: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "a"}, {Model: "b"}, {Provider: schemas.Gemini, Model: "c"}, {Model: "d"}}, want: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "a"}, {Provider: schemas.OpenAI, Model: "b"}, {Provider: schemas.Gemini, Model: "c"}, {Provider: schemas.OpenAI, Model: "d"}}, cloned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]schemas.Fallback(nil), tc.input...)
			got := resolveFallbackProviders(tc.provider, tc.input)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, original, tc.input, "caller-owned slice must stay unchanged")
			if len(got) > 0 {
				assert.Equal(t, tc.cloned, &got[0] != &tc.input[0])
			}
		})
	}
	t.Run("shared fallback slice supports concurrent routing", func(t *testing.T) {
		shared := []schemas.Fallback{{Model: "backup"}}
		var wg sync.WaitGroup
		for range 16 {
			for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic} {
				wg.Go(func() {
					resolved := resolveFallbackProviders(provider, shared)
					if resolved[0].Provider != provider {
						t.Errorf("got %s, want %s", resolved[0].Provider, provider)
					}
				})
			}
		}
		wg.Wait()
		assert.Empty(t, shared[0].Provider)
	})
	t.Run("unchanged chains allocate nothing", func(t *testing.T) {
		explicit := []schemas.Fallback{{Provider: schemas.OpenAI, Model: "backup"}}
		assert.Zero(t, testing.AllocsPerRun(100, func() { resolveFallbackProviders(schemas.OpenAI, nil) }))
		assert.Zero(t, testing.AllocsPerRun(100, func() { resolveFallbackProviders(schemas.OpenAI, explicit) }))
	})
}

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name                string
		url                 string
		allowPrivateNetwork bool
		wantErr             bool
		errMsg              string
	}{
		// Valid URLs
		{
			name:    "valid https URL",
			url:     "https://api.openai.com",
			wantErr: false,
		},
		{
			name:    "valid http URL",
			url:     "http://api.openai.com",
			wantErr: false,
		},
		{
			name:    "valid https URL with path",
			url:     "https://api.openai.com/v1",
			wantErr: false,
		},
		{
			name:    "valid https URL with port",
			url:     "https://api.openai.com:443",
			wantErr: false,
		},

		// Empty / malformed
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
			errMsg:  "URL cannot be empty",
		},
		{
			name:    "no scheme",
			url:     "api.openai.com",
			wantErr: true,
			errMsg:  "only https and http schemes are allowed",
		},
		{
			name:    "ftp scheme",
			url:     "ftp://api.openai.com",
			wantErr: true,
			errMsg:  "only https and http schemes are allowed",
		},
		{
			name:    "file scheme",
			url:     "file:///etc/passwd",
			wantErr: true,
			errMsg:  "only https and http schemes are allowed",
		},
		{
			name:    "missing hostname",
			url:     "http://",
			wantErr: true,
			errMsg:  "URL must have a hostname",
		},

		// Localhost / loopback — allowed for local deployments (Ollama, vLLM, SGL)
		{
			name:    "localhost",
			url:     "http://localhost",
			wantErr: false,
		},
		{
			name:    "localhost with port",
			url:     "http://localhost:8080",
			wantErr: false,
		},
		{
			name:    "loopback 127.0.0.1",
			url:     "http://127.0.0.1",
			wantErr: false,
		},
		{
			name:    "loopback ::1",
			url:     "http://[::1]",
			wantErr: false,
		},
		{
			name:    "all-zeros 0.0.0.0",
			url:     "http://0.0.0.0",
			wantErr: true,
			errMsg:  "unspecified IP addresses are not allowed",
		},

		// Private IP ranges (RFC 1918)
		{
			name:    "private 10.x.x.x",
			url:     "http://10.0.0.1",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},
		{
			name:    "private 172.16.x.x",
			url:     "http://172.16.0.1",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},
		{
			name:    "private 192.168.x.x",
			url:     "http://192.168.1.1",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},

		// Link-local / cloud metadata — always blocked even with AllowPrivateNetwork
		{
			name:    "AWS metadata service",
			url:     "http://169.254.169.254",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},
		{
			name:    "AWS metadata with path",
			url:     "http://169.254.169.254/latest/meta-data/",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},
		{
			name:    "link-local 169.254.x.x",
			url:     "http://169.254.1.1",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},

		// Query-parameter injection (the PoC vector from the advisory)
		{
			name:    "query param injection targeting metadata service",
			url:     "http://169.254.169.254/latest/meta-data/iam/security-credentials/role?x=",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},
		{
			name:    "query param injection targeting internal host",
			url:     "http://10.0.0.1/arbitrary/path?x=",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},

		// AllowPrivateNetwork=true: RFC 1918 allowed, link-local still blocked
		{
			name:                "allow_private_network permits 10.x.x.x",
			url:                 "http://10.0.0.5:8000",
			allowPrivateNetwork: true,
			wantErr:             false,
		},
		{
			name:                "allow_private_network permits 192.168.x.x",
			url:                 "http://192.168.1.50:11434",
			allowPrivateNetwork: true,
			wantErr:             false,
		},
		{
			name:                "allow_private_network still blocks 169.254.169.254",
			url:                 "http://169.254.169.254",
			allowPrivateNetwork: true,
			wantErr:             true,
			errMsg:              "link-local IP addresses are not allowed",
		},
		{
			name:                "allow_private_network still blocks 0.0.0.0",
			url:                 "http://0.0.0.0",
			allowPrivateNetwork: true,
			wantErr:             true,
			errMsg:              "unspecified IP addresses are not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.url, tt.allowPrivateNetwork)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %q", err.Error())
				}
			}
		})
	}
}

// TestPrepareContextForInternalEmbeddingRequestDisablesRawCapture ensures plugin-owned embeddings never inherit provider raw capture.
func TestPrepareContextForInternalEmbeddingRequestDisablesRawCapture(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	PrepareContextForInternalEmbeddingRequest(ctx)

	applyRawCaptureSignals(ctx, &schemas.ProviderConfig{
		SendBackRawRequest:      true,
		SendBackRawResponse:     true,
		StoreRawRequestResponse: true,
	})

	for key, name := range map[schemas.BifrostContextKey]string{
		schemas.BifrostContextKeyCaptureRawRequest:    "capture raw request",
		schemas.BifrostContextKeyCaptureRawResponse:   "capture raw response",
		schemas.BifrostContextKeyShouldStoreRawInLogs: "store raw request/response",
	} {
		if enabled, _ := ctx.Value(key).(bool); enabled {
			t.Errorf("%s = true, want false", name)
		}
	}
}

// TestValidateExternalURLBoundsDNSLookup guards against regressing to the package-level
// net.LookupIP (which has no context/deadline of its own, and previously let a stalled
// resolver block the caller indefinitely -- see externalURLDNSLookupTimeout's doc). A
// hostname under the .invalid TLD (reserved by RFC 2606 to never resolve) still exercises
// the real resolution path; the assertion is on wall-clock bound, not on the specific
// error, since what matters is that resolution can never run unbounded.
func TestValidateExternalURLBoundsDNSLookup(t *testing.T) {
	start := time.Now()
	err := ValidateExternalURL("https://this-host-does-not-exist-12345.invalid", false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error resolving a .invalid hostname, got nil")
	}
	// Generous margin above externalURLDNSLookupTimeout (5s): proves resolution is bounded
	// by that timeout rather than by an unrelated, much longer OS/runtime resolver ceiling.
	if elapsed > 8*time.Second {
		t.Errorf("ValidateExternalURL took %v to fail on an unresolvable host, want well under externalURLDNSLookupTimeout + margin", elapsed)
	}
}

// TestValidateExternalURLBoundsInFlightDNSLookup complements
// TestValidateExternalURLBoundsDNSLookup: a .invalid hostname NXDOMAINs almost immediately,
// so it never proves the timeout can cut off a lookup that's actually in flight -- the exact
// scenario externalURLDNSLookupTimeout exists to guard against (a hung/blackholed resolver).
// This substitutes a resolver that blocks until its context is canceled, so the only way
// ValidateExternalURL returns is via externalURLDNSLookupTimeout firing. Runs inside a
// synctest bubble -- context.WithTimeout is explicitly supported there (see the
// testing/synctest package docs' Context.WithTimeout example) -- so the 5s timeout resolves
// against a deterministic fake clock instead of a real 5-second wait.
func TestValidateExternalURLBoundsInFlightDNSLookup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := lookupIPAddr
		defer func() { lookupIPAddr = original }()

		lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		err := ValidateExternalURL("https://api.openai.com", false)
		if err == nil {
			t.Fatal("expected an error when the resolver blocks past the timeout, got nil")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected error to wrap context.DeadlineExceeded (proving externalURLDNSLookupTimeout fired), got %v", err)
		}
	})
}

// TestResolverLookupIPAddrRespectsContextDeadline pins down the stdlib behavior
// ValidateExternalURL's fix relies on: unlike the package-level net.LookupIP (which
// always uses context.Background() internally and ignores any external deadline),
// net.Resolver.LookupIPAddr honors a context deadline and returns promptly once it
// elapses, even against a real hostname that would otherwise resolve.
func TestResolverLookupIPAddrRespectsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	// Let the deadline definitely elapse before the lookup starts.
	time.Sleep(time.Millisecond)

	start := time.Now()
	_, err := (&net.Resolver{}).LookupIPAddr(ctx, "api.openai.com")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected LookupIPAddr to fail against an already-expired context, got nil error")
	}
	if elapsed > time.Second {
		t.Errorf("LookupIPAddr took %v to return after an already-expired deadline, want near-instant", elapsed)
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		hostname string
		want     bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"api.openai.com", false},
		{"10.0.0.1", false}, // private but not localhost — handled by IsPrivateIP
		{"169.254.169.254", false},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			if got := network.IsLocalhost(tt.hostname); got != tt.want {
				t.Errorf("IsLocalhost(%q) = %v, want %v", tt.hostname, got, tt.want)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// Private IPv4 (RFC 1918)
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		// Link-local
		{"169.254.0.1", true},
		{"169.254.169.254", true},
		// Loopback
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		// Public IPv4
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"104.21.0.1", false},
		// Private IPv6
		{"::1", true},     // loopback
		{"fe80::1", true}, // link-local
		{"fc00::1", true}, // unique local
		{"fd00::1", true}, // unique local
		// Public IPv6
		{"2606:4700::1", false},
		// Unspecified addresses (fail-closed)
		{"0.0.0.0", true},         // IPv4 unspecified
		{"0:0:0:0:0:0:0:0", true}, // IPv6 unspecified long form
		{"::", true},              // IPv6 unspecified short form
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := network.IsPrivateIP(ip); got != tt.want {
				t.Errorf("IsPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// Failing over starts a fresh attempt, so the state the previous attempt resolved for itself must not
// survive into it. What the request may reach is not among it: that is a fact about the caller, not
// the attempt, and the presented credential and the caller's identity survive with it so the limits
// of the next attempt can be resolved against the same access.
func TestClearCtxForFallback(t *testing.T) {
	cleared := []schemas.BifrostContextKey{
		schemas.BifrostContextKeyAPIKeyID,
		schemas.BifrostContextKeyAPIKeyName,
		schemas.BifrostContextKeyGovernanceIncludeOnlyKeys,
		schemas.BifrostContextKeyChangeRequestType,
		schemas.BifrostContextKeyAttemptTrail,
		schemas.BifrostContextKeyStreamEndIndicator,
		schemas.BifrostContextKeyConnectionClosed,
		schemas.BifrostContextKeyStreamBodyExhausted,
		schemas.BifrostContextKeyStreamParkedAfterFinish,
		schemas.BifrostContextKeySupportsAssistantPrefill,
		// Set by the Bedrock InvokeModel stream path; a fallback to a plain-SSE
		// provider must not inherit the AWS event-stream reader (#6825).
		schemas.BifrostContextKeySSEReaderFactory,
		// The load balancer's record of the attempt that just failed.
		schemas.BifrostContextKeyLoadBalancerAttempt,
	}
	// The next attempt resolves its own limits, which needs the same credential and caller.
	preserved := []schemas.BifrostContextKey{
		schemas.BifrostContextKeyVirtualKey,
		schemas.BifrostContextKeyUserID,
		// What the caller sent is the same for every attempt of the request.
		schemas.BifrostContextKeyRequestedProvider,
		schemas.BifrostContextKeyRequestedModel,
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, key := range append(append([]schemas.BifrostContextKey{}, cleared...), preserved...) {
		ctx.SetValue(key, "set")
	}

	clearCtxForFallback(ctx)

	for _, key := range cleared {
		if ctx.Value(key) != nil {
			t.Errorf("%v survived into the next attempt", key)
		}
	}
	for _, key := range preserved {
		if ctx.Value(key) == nil {
			t.Errorf("%v was cleared, leaving the next attempt unable to resolve its own limits", key)
		}
	}
}

// TestCanProviderKeyValueBeEmptyGithubCopilot pins that a GitHub Copilot key needs a value:
// every way to authenticate, including Sign in with GitHub, ends with a token in it.
func TestCanProviderKeyValueBeEmptyGithubCopilot(t *testing.T) {
	if CanProviderKeyValueBeEmpty(schemas.GithubCopilot) {
		t.Fatal("GitHub Copilot keys always carry a token; the allowlist must not include the provider")
	}
}

// TestStampRequestedRoute pins what core records as the caller's route before any
// PreRequestHook runs: plugins read it to report the model a routing rule rewrote.
func TestStampRequestedRoute(t *testing.T) {
	requested := func(ctx *schemas.BifrostContext) (schemas.ModelProvider, string) {
		provider, _ := ctx.Value(schemas.BifrostContextKeyRequestedProvider).(schemas.ModelProvider)
		model, _ := ctx.Value(schemas.BifrostContextKeyRequestedModel).(string)
		return provider, model
	}

	t.Run("records the caller's provider and model", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		stampRequestedRoute(ctx, schemas.Route{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
		provider, model := requested(ctx)
		assert.Equal(t, schemas.OpenAI, provider)
		assert.Equal(t, "gpt-4o-mini", model)
	})

	// Large-payload requests carry the model only in the metadata, and the routing plugin
	// rewrites it there in place, so the value must be copied before the hooks run.
	t.Run("large payload model is copied before routing rewrites it", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		metadata := &schemas.LargePayloadMetadata{Model: "gpt-4o-mini"}
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
		stampRequestedRoute(ctx, schemas.Route{Provider: schemas.OpenAI})
		metadata.Model = "gpt-4o"
		_, model := requested(ctx)
		assert.Equal(t, "gpt-4o-mini", model)
	})

	// bifrost.ctx is shared by every nil-ctx caller, so an empty provider must overwrite the
	// previous request's rather than leave it in place.
	t.Run("reused context does not keep the previous request's route", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		stampRequestedRoute(ctx, schemas.Route{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
		stampRequestedRoute(ctx, schemas.Route{Model: "claude-sonnet-4-6"})
		provider, model := requested(ctx)
		assert.Empty(t, provider)
		assert.Equal(t, "claude-sonnet-4-6", model)
	})
}

// TestValidateKeyGithubCopilot pins that a GitHub Copilot key without a token is refused at
// setup rather than at the first inference call.
func TestValidateKeyGithubCopilot(t *testing.T) {
	assert.NoError(t, validateKey(schemas.GithubCopilot, &schemas.Key{Value: *schemas.NewSecretVar("github_pat_abc")}))
	assert.False(t, keyValueMayBeEmpty(schemas.GithubCopilot, &schemas.Key{}), "a key with no token is not a credential")
}

func TestValidateKeyOAuthKeyConfig(t *testing.T) {
	cc := func(mutate func(*schemas.OAuthKeyConfig)) *schemas.OAuthKeyConfig {
		c := &schemas.OAuthKeyConfig{
			GrantType:    schemas.OAuthGrantClientCredentials,
			TokenURL:     *schemas.NewSecretVar("https://idp.example/oauth2/token"),
			ClientID:     schemas.NewSecretVar("id"),
			ClientSecret: schemas.NewSecretVar("secret"),
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	jb := func(mutate func(*schemas.OAuthKeyConfig)) *schemas.OAuthKeyConfig {
		c := &schemas.OAuthKeyConfig{
			GrantType:  schemas.OAuthGrantJWTBearer,
			TokenURL:   *schemas.NewSecretVar("https://idp.example/oauth2/token"),
			PrivateKey: schemas.NewSecretVar("pem"),
			Issuer:     "client",
			Audience:   "https://idp.example/oauth2/token",
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}

	tests := []struct {
		name      string
		key       schemas.Key
		wantError string
	}{
		{name: "client_credentials is sufficient", key: schemas.Key{OAuthKeyConfig: cc(nil)}},
		{name: "jwt_bearer is sufficient", key: schemas.Key{OAuthKeyConfig: jb(nil)}},
		// Key selection runs this on every request. A static value always wins over the block, so
		// an incomplete block must not take a usable key out of rotation; the management API
		// still refuses to save one (handlers.validateProviderKey).
		{name: "a value alongside an incomplete config keeps the key usable", key: schemas.Key{Value: *schemas.NewSecretVar("sk"), OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.ClientSecret = nil })}},
		{name: "missing grant", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.GrantType = "" })}, wantError: "grant_type is required"},
		{name: "unknown grant", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.GrantType = "password" })}, wantError: "must be client_credentials or jwt_bearer"},
		{name: "missing token url", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar(" ") })}, wantError: "token_url is required"},
		{name: "relative token url", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("/token") })}, wantError: "must be https"},
		{name: "plain http on a public host", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("http://idp.example/token") })}, wantError: "must be https"},
		{name: "token url with userinfo", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) {
			c.TokenURL = *schemas.NewSecretVar("https://user:pass@idp.example/token")
		})}, wantError: "must not carry userinfo, a query, or a fragment"},
		{name: "token url with a query", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) {
			c.TokenURL = *schemas.NewSecretVar("https://idp.example/token?client_secret=x")
		})}, wantError: "must not carry userinfo, a query, or a fragment"},
		{name: "token url with a fragment", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("https://idp.example/token#frag") })}, wantError: "must not carry userinfo, a query, or a fragment"},
		{name: "plain http on loopback is accepted", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("http://127.0.0.1:9/token") })}},
		{name: "jwt_bearer negative lifetime names the accepted range", key: schemas.Key{OAuthKeyConfig: jb(func(c *schemas.OAuthKeyConfig) { c.AssertionLifetimeSeconds = -1 })}, wantError: "between 0 (default) and 3600"},
		{name: "missing client id", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.ClientID = schemas.NewSecretVar(" ") })}, wantError: "client_id and oauth_key_config.client_secret are required"},
		{name: "bad auth style", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) { c.AuthStyle = "query" })}, wantError: "auth_style must be header or body"},
		{name: "jwt_bearer missing key", key: schemas.Key{OAuthKeyConfig: jb(func(c *schemas.OAuthKeyConfig) { c.PrivateKey = nil })}, wantError: "private_key is required"},
		{name: "jwt_bearer missing issuer", key: schemas.Key{OAuthKeyConfig: jb(func(c *schemas.OAuthKeyConfig) { c.Issuer = "" })}, wantError: "issuer is required"},
		{name: "jwt_bearer missing audience", key: schemas.Key{OAuthKeyConfig: jb(func(c *schemas.OAuthKeyConfig) { c.Audience = "" })}, wantError: "audience is required"},
		{name: "jwt_bearer symmetric algorithm", key: schemas.Key{OAuthKeyConfig: jb(func(c *schemas.OAuthKeyConfig) { c.SigningAlgorithm = "HS256" })}, wantError: "signing_algorithm must be one of"},
		{name: "jwt_bearer lifetime too long", key: schemas.Key{OAuthKeyConfig: jb(func(c *schemas.OAuthKeyConfig) { c.AssertionLifetimeSeconds = 7200 })}, wantError: "assertion_lifetime_seconds"},
		{name: "env-backed references count as present", key: schemas.Key{OAuthKeyConfig: cc(func(c *schemas.OAuthKeyConfig) {
			c.TokenURL = *schemas.NewSecretVar("env.IDP_TOKEN_URL_UNSET_IN_TEST")
			c.ClientSecret = schemas.NewSecretVar("env.IDP_SECRET_UNSET_IN_TEST")
		})}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := tt.key
			err := validateKey(schemas.OpenAI, &key)
			if tt.wantError == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}

	t.Run("a key with oauth_key_config may have no value on an openai-based provider", func(t *testing.T) {
		assert.True(t, keyValueMayBeEmpty(schemas.OpenAI, &schemas.Key{OAuthKeyConfig: cc(nil)}))
		assert.False(t, keyValueMayBeEmpty(schemas.OpenAI, &schemas.Key{}))
		assert.False(t, keyValueMayBeEmpty(schemas.Anthropic, &schemas.Key{OAuthKeyConfig: cc(nil)}))
		assert.True(t, keyValueMayBeEmpty(schemas.Databricks, &schemas.Key{}))
	})

	t.Run("oauth_key_config is refused on a provider that does not resolve it", func(t *testing.T) {
		key := schemas.Key{OAuthKeyConfig: cc(nil)}
		err := validateKey(schemas.Anthropic, &key)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported on openai")
	})
}

// A request cancelled at the same instant its backoff expires must still be
// reported as cancelled: Go's select picks uniformly among ready cases, so a
// timer-only check lets roughly half of such requests run one more attempt
// (maximhq/bifrost#7105 review). With a cancelled ctx and a zero backoff both
// cases are ready on every call, so 64 calls expose a missing ctx check with
// probability 1 - 2^-64.
func TestWaitRetryBackoffReportsCancellationAtTimerExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 64; i++ {
		if !waitRetryBackoff(ctx, 0) {
			t.Fatalf("call %d: backoff expired together with the cancel and the wait reported not cancelled", i)
		}
	}
}

// OpenAI-compatible clients take the assistant role from the first chunk of a
// choice and drop tool calls without it (maximhq/bifrost#7693). Some clients
// concatenate a repeated role, so it must appear exactly once.
func TestWrapConvertedStreamPostHookRunnerSendsAssistantRoleOnce(t *testing.T) {
	reasoning := schemas.ResponsesMessageTypeReasoning
	functionCall := schemas.ResponsesMessageTypeFunctionCall
	message := schemas.ResponsesMessageTypeMessage

	tests := []struct {
		name   string
		events []*schemas.BifrostResponsesStreamResponse
	}{
		{
			name: "tool call turn without a message item",
			events: []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated},
				{Type: schemas.ResponsesStreamResponseTypeInProgress},
				{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: &schemas.ResponsesMessage{Type: &reasoning}},
				{
					Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
					OutputIndex: schemas.Ptr(1),
					Item: &schemas.ResponsesMessage{
						Type: &functionCall,
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("call_1"),
							Name:   schemas.Ptr("get_weather"),
						},
					},
				},
				{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, Delta: schemas.Ptr("{}")},
				{Type: schemas.ResponsesStreamResponseTypeCompleted},
			},
		},
		{
			name: "text turn",
			events: []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated},
				{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: &schemas.ResponsesMessage{Type: &message}},
				{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, Delta: schemas.Ptr("Hello")},
				{Type: schemas.ResponsesStreamResponseTypeCompleted},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var roles []*string
			runner := wrapConvertedStreamPostHookRunner(func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				require.NotNil(t, result.ChatResponse)
				require.NotEmpty(t, result.ChatResponse.Choices)
				roles = append(roles, result.ChatResponse.Choices[0].Delta.Role)
				return result, bifrostErr
			}, schemas.ResponsesRequest)

			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			for _, event := range tt.events {
				runner(ctx, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
			}

			require.Len(t, roles, len(tt.events))
			require.NotNil(t, roles[0], "first chunk has no role")
			assert.Equal(t, string(schemas.ChatMessageRoleAssistant), *roles[0])
			for i, role := range roles[1:] {
				assert.Nil(t, role, "chunk %d repeats the role", i+1)
			}
		})
	}
}

// Only the response.created, in_progress and completed events carry the Response
// object, so the deltas between them convert with no id, model or created. OpenAI
// SDK stream accumulators drop a chunk whose id differs from the stream's, which
// left the streamed text empty.
func TestWrapConvertedStreamPostHookRunnerKeepsStreamIdentityOnEveryChunk(t *testing.T) {
	message := schemas.ResponsesMessageTypeMessage
	functionCall := schemas.ResponsesMessageTypeFunctionCall
	upstream := &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_0dd8"), Model: "gpt-6-luna", CreatedAt: 1760000000}
	tests := []struct {
		name   string
		events []*schemas.BifrostResponsesStreamResponse
	}{
		{
			name: "text turn",
			events: []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated, Response: upstream},
				{Type: schemas.ResponsesStreamResponseTypeInProgress, Response: upstream},
				{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: &schemas.ResponsesMessage{Type: &message}},
				{Type: schemas.ResponsesStreamResponseTypeContentPartAdded},
				{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, Delta: schemas.Ptr("ok")},
				{Type: schemas.ResponsesStreamResponseTypeOutputTextDone},
				{Type: schemas.ResponsesStreamResponseTypeOutputItemDone},
				{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: upstream},
			},
		},
		{
			name: "tool call turn",
			events: []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated, Response: upstream},
				{
					Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
					Item: &schemas.ResponsesMessage{
						Type:                 &functionCall,
						ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("get_weather")},
					},
				},
				{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, Delta: schemas.Ptr("{}")},
				{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: upstream},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chunks []*schemas.BifrostChatResponse
			runner := wrapConvertedStreamPostHookRunner(func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				require.NotNil(t, result.ChatResponse)
				chunks = append(chunks, result.ChatResponse)
				return result, bifrostErr
			}, schemas.ResponsesRequest)

			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			for _, event := range tt.events {
				runner(ctx, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
			}

			require.Len(t, chunks, len(tt.events))
			for i, chunk := range chunks {
				assert.Equal(t, "resp_0dd8", chunk.ID, "chunk %d (%s) id", i, tt.events[i].Type)
				assert.Equal(t, "gpt-6-luna", chunk.Model, "chunk %d (%s) model", i, tt.events[i].Type)
				assert.Equal(t, 1760000000, chunk.Created, "chunk %d (%s) created", i, tt.events[i].Type)
			}
		})
	}
}

// BenchmarkWrapConvertedStreamPostHookRunner measures one converted text turn,
// created through completed, as a chat stream served through Responses sees it.
func BenchmarkWrapConvertedStreamPostHookRunner(b *testing.B) {
	message := schemas.ResponsesMessageTypeMessage
	upstream := &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_0dd8"), Model: "gpt-6-luna", CreatedAt: 1760000000}
	events := []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: upstream},
		{Type: schemas.ResponsesStreamResponseTypeInProgress, Response: upstream},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: &schemas.ResponsesMessage{Type: &message}},
		{Type: schemas.ResponsesStreamResponseTypeContentPartAdded},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, Delta: schemas.Ptr("ok")},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDone},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone},
		{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: upstream},
	}
	noop := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, bifrostErr
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner := wrapConvertedStreamPostHookRunner(noop, schemas.ResponsesRequest)
		for _, event := range events {
			runner(ctx, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
		}
	}
}

// A converted stream chunk still carries the upstream API's raw bytes, so it must
// say it was converted: integrations echo RawResponse only for unconverted chunks.
func TestWrapConvertedStreamPostHookRunnerMarksConvertedChunks(t *testing.T) {
	tests := []struct {
		name       string
		targetType schemas.RequestType
		result     *schemas.BifrostResponse
		extra      func(*schemas.BifrostResponse) *schemas.BifrostResponseExtraFields
	}{
		{
			name:       "chat served through responses",
			targetType: schemas.ResponsesRequest,
			result: &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:        schemas.ResponsesStreamResponseTypeCreated,
				ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI, RawResponse: `{"type":"response.created"}`},
			}},
			extra: func(r *schemas.BifrostResponse) *schemas.BifrostResponseExtraFields {
				return &r.ChatResponse.ExtraFields
			},
		},
		{
			name:       "text served through chat",
			targetType: schemas.ChatCompletionRequest,
			result: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
				ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI, RawResponse: `{"object":"chat.completion.chunk"}`},
			}},
			extra: func(r *schemas.BifrostResponse) *schemas.BifrostResponseExtraFields {
				return &r.TextCompletionResponse.ExtraFields
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *schemas.BifrostResponse
			runner := wrapConvertedStreamPostHookRunner(func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				got = result
				return result, bifrostErr
			}, tt.targetType)
			runner(schemas.NewBifrostContext(context.Background(), time.Time{}), tt.result, nil)
			require.NotNil(t, got)
			assert.Equal(t, tt.targetType, tt.extra(got).ConvertedRequestType)
		})
	}
}

func TestCopyInferenceRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *schemas.BifrostRequest
	}{
		{name: "TextCompletionRequest", req: &schemas.BifrostRequest{TextCompletionRequest: &schemas.BifrostTextCompletionRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "ChatRequest", req: &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "ResponsesRequest", req: &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "CountTokensRequest", req: &schemas.BifrostRequest{CountTokensRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "CompactionRequest", req: &schemas.BifrostRequest{CompactionRequest: &schemas.BifrostCompactionRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "EmbeddingRequest", req: &schemas.BifrostRequest{EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "RerankRequest", req: &schemas.BifrostRequest{RerankRequest: &schemas.BifrostRerankRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "DecisionRequest", req: &schemas.BifrostRequest{DecisionRequest: &schemas.BifrostDecisionRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "OCRRequest", req: &schemas.BifrostRequest{OCRRequest: &schemas.BifrostOCRRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "SpeechRequest", req: &schemas.BifrostRequest{SpeechRequest: &schemas.BifrostSpeechRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "TranscriptionRequest", req: &schemas.BifrostRequest{TranscriptionRequest: &schemas.BifrostTranscriptionRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "ImageGenerationRequest", req: &schemas.BifrostRequest{ImageGenerationRequest: &schemas.BifrostImageGenerationRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "VideoGenerationRequest", req: &schemas.BifrostRequest{VideoGenerationRequest: &schemas.BifrostVideoGenerationRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "VideoEditRequest", req: &schemas.BifrostRequest{VideoEditRequest: &schemas.BifrostVideoEditRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "ImageEditRequest", req: &schemas.BifrostRequest{ImageEditRequest: &schemas.BifrostImageEditRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
		{name: "ImageVariationRequest", req: &schemas.BifrostRequest{ImageVariationRequest: &schemas.BifrostImageVariationRequest{Provider: schemas.OpenAI, Model: "primary", Fallbacks: []schemas.Fallback{{Model: "backup"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned := copyInferenceRequest(tc.req, nil)
			owned.SetFallbacks([]schemas.Fallback{{Provider: schemas.Cerebras, Model: "backup"}})
			owned.SetProvider(schemas.Cerebras)
			owned.SetModel("owned")
			provider, model, fallbacks := tc.req.GetRequestFields()
			if provider != schemas.OpenAI || model != "primary" || len(fallbacks) != 1 || fallbacks[0].Provider != "" {
				t.Fatalf("caller body was changed: %s/%s %+v", provider, model, fallbacks)
			}
			target := schemas.Fallback{Provider: schemas.Anthropic, Model: "target"}
			attempt := copyInferenceRequest(tc.req, &target)
			provider, model, _ = attempt.GetRequestFields()
			if provider != target.Provider || model != target.Model {
				t.Fatalf("fallback arm not copied/routed: %s/%s", provider, model)
			}
		})
	}
	t.Run("distinct inference arms retain their own values when not retargeting", func(t *testing.T) {
		req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "chat"}, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "responses"}}
		owned := copyInferenceRequest(req, nil)
		if owned.ChatRequest == req.ChatRequest || owned.ResponsesRequest == req.ResponsesRequest || owned.ChatRequest.Model != "chat" || owned.ResponsesRequest.Model != "responses" {
			t.Fatal("arm identity or model lost")
		}
		target := schemas.Fallback{Provider: schemas.Cerebras, Model: "target"}
		attempt := copyInferenceRequest(req, &target)
		if attempt.ChatRequest.Provider != target.Provider || attempt.ResponsesRequest.Provider != target.Provider || attempt.ChatRequest.Model != target.Model || attempt.ResponsesRequest.Model != target.Model {
			t.Fatal("existing multi-arm retargeting behavior changed")
		}
	})
	t.Run("payloads and parameter pointers stay shared", func(t *testing.T) {
		req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "primary", Params: &schemas.ChatParameters{}, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}}, RawRequestBody: []byte(`{"model":"primary"}`)}}
		owned := copyInferenceRequest(req, nil)
		if owned.ChatRequest == req.ChatRequest || owned.ChatRequest.Params != req.ChatRequest.Params || &owned.ChatRequest.Input[0] != &req.ChatRequest.Input[0] || &owned.ChatRequest.RawRequestBody[0] != &req.ChatRequest.RawRequestBody[0] {
			t.Fatal("request header was not isolated, or unchanged payloads were deep-copied")
		}
	})
}
