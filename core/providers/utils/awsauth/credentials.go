// Package awsauth resolves AWS credentials and signs HTTP requests with SigV4 for every caller
// that talks to AWS with IAM: the Bedrock and SageMaker providers, relays and MCP clients.
package awsauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Config is a resolved AWS credential source. With no access key or secret key the default
// credential chain is used (environment, shared files, Pod Identity, IRSA, instance profile);
// RoleARN assumes a role on top of either.
type Config struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
	RoleARN      string
	ExternalID   string
	SessionName  string
}

// defaultSessionName is the STS role session name when Config.SessionName is empty.
const defaultSessionName = "bifrost-session"

// expiryWindow refreshes cached credentials this long before they expire, as the SDK does for
// container credentials, so a request never carries credentials about to lapse.
const expiryWindow = 5 * time.Minute

func withExpiryWindow(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = expiryWindow }

// defaultChainProviders caches one default-chain provider per region, so the chain is loaded
// once and its credentials are fetched again only when they near expiry.
var defaultChainProviders sync.Map // region -> *chainSource

// chainSource is a cached default-chain provider. A failed fetch drops it, so the next lookup loads
// the chain again and can pick a source that appeared since, such as a credentials file.
type chainSource struct {
	aws.CredentialsProvider
	region string
}

func (s *chainSource) Retrieve(ctx context.Context) (aws.Credentials, error) {
	creds, err := s.CredentialsProvider.Retrieve(ctx)
	if err != nil {
		defaultChainProviders.CompareAndDelete(s.region, s)
	}
	return creds, err
}

// assumeRoleProviders caches one AssumeRole provider per role and source credential, so STS
// is called only when the assumed credentials near expiry.
var assumeRoleProviders sync.Map // assumeRoleKey -> *aws.CredentialsCache

// usesDefaultChain reports whether cfg carries no static keys.
func (cfg Config) usesDefaultChain() bool {
	return cfg.AccessKey == "" && cfg.SecretKey == ""
}

// CredentialsProvider returns the provider for cfg in region. Static keys need no lookup; the
// default chain and assumed roles come from a process-wide cache, so a warm call is one map load.
func CredentialsProvider(ctx context.Context, cfg Config, region string) (aws.CredentialsProvider, error) {
	source, err := sourceProvider(ctx, cfg, region)
	if err != nil || cfg.RoleARN == "" {
		return source, err
	}
	key := assumeRoleKey(cfg, region)
	if cached, ok := assumeRoleProviders.Load(key); ok {
		return cached.(*aws.CredentialsCache), nil
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithCredentialsProvider(source))
	if err != nil {
		return nil, err
	}
	// STS signs with the region's current source, not the one at build time: a failed fetch
	// replaces it, and Invalidate must reach the source the role uses.
	awsCfg.Credentials = aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		current, err := sourceProvider(ctx, cfg, region)
		if err != nil {
			return aws.Credentials{}, err
		}
		return current.Retrieve(ctx)
	})
	sessionName := cfg.SessionName
	if sessionName == "" {
		sessionName = defaultSessionName
	}
	provider := aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(awsCfg), cfg.RoleARN, func(o *stscreds.AssumeRoleOptions) {
		if cfg.ExternalID != "" {
			o.ExternalID = aws.String(cfg.ExternalID)
		}
		o.RoleSessionName = sessionName
	}), withExpiryWindow)
	actual, _ := assumeRoleProviders.LoadOrStore(key, provider)
	return actual.(*aws.CredentialsCache), nil
}

// Invalidate makes the next lookup for cfg in region fetch fresh credentials, for when AWS
// rejected the cached ones before their expiry. Static keys hold nothing to invalidate.
func Invalidate(cfg Config, region string) {
	if cfg.RoleARN != "" {
		if cached, ok := assumeRoleProviders.Load(assumeRoleKey(cfg, region)); ok {
			cached.(*aws.CredentialsCache).Invalidate()
		}
	}
	if cfg.usesDefaultChain() {
		// A shared-file source reads the file on each use, so it holds nothing to invalidate.
		if cached, ok := defaultChainProviders.Load(region); ok {
			if cache, ok := cached.(*chainSource).CredentialsProvider.(*aws.CredentialsCache); ok {
				cache.Invalidate()
			}
		}
	}
}

// sourceProvider returns the credentials a role is assumed with, or used directly without one.
func sourceProvider(ctx context.Context, cfg Config, region string) (aws.CredentialsProvider, error) {
	if !cfg.usesDefaultChain() {
		// Not credentials.NewStaticCredentialsProvider: it refuses a half-filled pair, which has
		// always been signed and left for AWS to reject.
		static := aws.Credentials{AccessKeyID: cfg.AccessKey, SecretAccessKey: cfg.SecretKey, SessionToken: cfg.SessionToken, Source: "StaticCredentials"}
		return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return static, nil }), nil
	}
	if cached, ok := defaultChainProviders.Load(region); ok {
		return cached.(*chainSource), nil
	}
	// The window also applies to the caches the SDK builds itself, which refresh IRSA and IMDS
	// credentials only at expiry.
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithCredentialsCacheOptions(withExpiryWindow))
	if err != nil {
		return nil, err
	}
	if awsCfg.Credentials == nil {
		return nil, errors.New("no aws credential provider found")
	}
	provider, ok := awsCfg.Credentials.(*aws.CredentialsCache)
	if !ok {
		provider = aws.NewCredentialsCache(awsCfg.Credentials, withExpiryWindow)
	}
	reload := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		reloaded, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			return aws.Credentials{}, err
		}
		if reloaded.Credentials == nil {
			return aws.Credentials{}, errors.New("no aws credential provider found")
		}
		return reloaded.Credentials.Retrieve(ctx)
	})
	var source aws.CredentialsProvider = provider
	switch sources := provider.ProviderSources(); {
	case provider.IsCredentialsProvider(credentials.StaticCredentialsProvider{}) && slices.Contains(sources, aws.CredentialSourceProfile):
		// Shared-file keys are read once at load; read the file again on each use so a rotated
		// file is picked up, as Bedrock always did. Refreshable sources stay cached.
		source = reload
	case slices.Contains(sources, aws.CredentialSourceProfileSourceProfile):
		// A source_profile role holds the keys read at load; read them again on each refresh,
		// so rotated keys are used while STS is still called only near expiry.
		source = aws.NewCredentialsCache(reload, withExpiryWindow)
	}
	actual, _ := defaultChainProviders.LoadOrStore(region, &chainSource{CredentialsProvider: source, region: region})
	return actual.(*chainSource), nil
}

// assumeRoleKey identifies an assumed role by its parameters and source credential. The source
// is the access key plus a short hash of the secret and session token, never either itself, so
// a corrected or rotated secret gets a provider of its own.
func assumeRoleKey(cfg Config, region string) string {
	source := "default_chain"
	if !cfg.usesDefaultChain() {
		pairHash := sha256.Sum256([]byte(cfg.AccessKey + "\x00" + cfg.SecretKey + "\x00" + cfg.SessionToken))
		source = cfg.AccessKey + "|" + hex.EncodeToString(pairHash[:8])
	}
	sessionName := cfg.SessionName
	if sessionName == "" {
		sessionName = defaultSessionName
	}
	return strings.Join([]string{region, cfg.RoleARN, cfg.ExternalID, sessionName, source}, "|")
}
