package vertex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/google/externalaccount"
)

// This file implements GCP Workload Identity Federation from an AWS identity for the Vertex provider.
//
// The Google externalaccount library can already exchange a signed AWS STS GetCallerIdentity request
// for a GCP token, but its built-in AWS credential source only looks at AWS_ACCESS_KEY_ID-style env
// vars or the EC2 instance metadata service. On EKS that means IRSA and Pod Identity are invisible and
// the exchange either fails or silently runs as the node role. Bifrost therefore supplies the subject
// token itself: AWS credentials come from the AWS SDK default chain (which understands IRSA, Pod
// Identity, ECS task roles, instance profiles and env vars), the GetCallerIdentity request is signed
// with the SDK's SigV4 signer against a partition-aware STS host, and the result is serialized in the
// wire format the GCP Security Token Service expects.

const (
	// awsSubjectTokenType is the STS subject_token_type for a signed AWS GetCallerIdentity request.
	awsSubjectTokenType = "urn:ietf:params:aws:token-type:aws4_request"
	// awsSTSGetCallerIdentityQuery is the fixed query string of the request that GCP re-plays against AWS.
	awsSTSGetCallerIdentityQuery = "Action=GetCallerIdentity&Version=2011-06-15"
	// awsFederationSessionName is the STS session name used for the optional aws_role_arn hop.
	awsFederationSessionName = "bifrost-vertex-federation"
	// awsFederationIMDSTimeout bounds the instance-metadata region lookup, which is the last resort.
	awsFederationIMDSTimeout = 2 * time.Second
	// emptyPayloadSHA256 is the SHA-256 of an empty body; GetCallerIdentity is sent without one.
	emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// gcpTargetResourceHeader binds the signature to the workload identity provider (recommended by GCP).
	gcpTargetResourceHeader = "x-goog-cloud-target-resource"
)

// gcpSTSTokenURL and gcpIAMCredentialsBaseURL are variables so tests can point them at local fakes.
var (
	gcpSTSTokenURL           = "https://sts.googleapis.com/v1/token"
	gcpIAMCredentialsBaseURL = "https://iamcredentials.googleapis.com"
)

// awsSTSHost returns the regional STS endpoint for the AWS partition the region belongs to.
// GCP replays the signed request against this host, so it must be the regional endpoint.
func awsSTSHost(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "sts." + region + ".amazonaws.com.cn"
	}
	return "sts." + region + ".amazonaws.com"
}

// awsWorkloadIdentityCacheIdentity returns a canonical string identifying a federation config so
// two keys with the same settings share one token source and different settings never collide.
func awsWorkloadIdentityCacheIdentity(cfg *schemas.VertexAWSWorkloadIdentityConfig) string {
	return strings.Join([]string{
		"aws-wif",
		cfg.Audience.GetValue(),
		cfg.ServiceAccountEmail.GetValue(),
		cfg.AWSRegion.GetValue(),
		cfg.AWSRoleARN.GetValue(),
		fmt.Sprint(cfg.TokenLifetimeSeconds),
	}, "|")
}

// newAWSFederatedTokenSource builds a token source that federates the workload's AWS identity into a
// GCP access token through the Workload Identity Pool provider named by cfg.Audience.
func newAWSFederatedTokenSource(ctx context.Context, cfg *schemas.VertexAWSWorkloadIdentityConfig) (oauth2.TokenSource, error) {
	if !cfg.IsSet() {
		return nil, fmt.Errorf("vertex aws federation: audience is required")
	}
	supplier, err := newAWSSubjectTokenSupplier(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newAWSFederatedTokenSourceWithSupplier(ctx, cfg, supplier)
}

// newAWSFederatedTokenSourceWithSupplier wires a subject-token supplier into the Google external
// account flow. The library caches and refreshes the resulting tokens, so no extra wrapping is needed.
// ctx is kept by the token source for every exchange and refresh, so an oauth2.HTTPClient on it
// (the provider's proxy-aware auth client) carries the STS and impersonation calls.
func newAWSFederatedTokenSourceWithSupplier(ctx context.Context, cfg *schemas.VertexAWSWorkloadIdentityConfig, supplier externalaccount.SubjectTokenSupplier) (oauth2.TokenSource, error) {
	conf := externalaccount.Config{
		Audience:             cfg.Audience.GetValue(),
		SubjectTokenType:     awsSubjectTokenType,
		TokenURL:             gcpSTSTokenURL,
		Scopes:               []string{cloudPlatformScope},
		SubjectTokenSupplier: supplier,
	}
	if email := cfg.ServiceAccountEmail.GetValue(); email != "" {
		conf.ServiceAccountImpersonationURL = fmt.Sprintf("%s/v1/projects/-/serviceAccounts/%s:generateAccessToken",
			gcpIAMCredentialsBaseURL, url.PathEscape(email))
		conf.ServiceAccountImpersonationLifetimeSeconds = cfg.TokenLifetimeSeconds
	}
	ts, err := externalaccount.NewTokenSource(ctx, conf)
	if err != nil {
		return nil, fmt.Errorf("vertex aws federation: gcp token exchange setup: %w", err)
	}
	return ts, nil
}

// awsSubjectTokenSupplier produces the signed GetCallerIdentity request that GCP STS accepts as a
// subject token. It implements externalaccount.SubjectTokenSupplier.
type awsSubjectTokenSupplier struct {
	audience       string
	creds          aws.CredentialsProvider
	regionOverride string
	sdkRegion      string
	imdsRegion     func(ctx context.Context) (string, error)
	signer         *v4.Signer
	now            func() time.Time

	regionMu sync.Mutex
	region   string
}

// newAWSSubjectTokenSupplier resolves AWS credentials through the SDK default chain, optionally
// assumes cfg.AWSRoleARN on top, and prepares region resolution.
func newAWSSubjectTokenSupplier(ctx context.Context, cfg *schemas.VertexAWSWorkloadIdentityConfig) (*awsSubjectTokenSupplier, error) {
	// A configured aws_region must reach the SDK clients too (the optional AssumeRole hop, the IMDS
	// probe), not only the final signature: without an ambient region STS cannot resolve an endpoint.
	var loadOpts []func(*config.LoadOptions) error
	if region := cfg.AWSRegion.GetValue(); region != "" {
		loadOpts = append(loadOpts, config.WithRegion(region))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("vertex aws federation: aws credentials: %w", err)
	}
	creds := awsCfg.Credentials
	if roleARN := cfg.AWSRoleARN.GetValue(); roleARN != "" {
		creds = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(
			sts.NewFromConfig(awsCfg), roleARN,
			func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = awsFederationSessionName },
		))
	}
	imdsClient := imds.NewFromConfig(awsCfg)
	s := newAWSSubjectTokenSupplierWithCredentials(cfg.Audience.GetValue(), creds, cfg.AWSRegion.GetValue())
	s.sdkRegion = awsCfg.Region
	s.imdsRegion = func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, awsFederationIMDSTimeout)
		defer cancel()
		out, err := imdsClient.GetRegion(ctx, &imds.GetRegionInput{})
		if err != nil {
			return "", err
		}
		return out.Region, nil
	}
	return s, nil
}

// newAWSSubjectTokenSupplierWithCredentials builds a supplier around explicit credentials and region.
// Production code goes through newAWSSubjectTokenSupplier; this constructor exists for tests.
func newAWSSubjectTokenSupplierWithCredentials(audience string, creds aws.CredentialsProvider, region string) *awsSubjectTokenSupplier {
	return &awsSubjectTokenSupplier{
		audience:       audience,
		creds:          creds,
		regionOverride: region,
		signer:         v4.NewSigner(),
		now:            time.Now,
	}
}

// resolveRegion picks the signing region: explicit config, then the SDK-resolved region
// (AWS_REGION, AWS_DEFAULT_REGION, shared config), then instance metadata. The result is cached.
func (s *awsSubjectTokenSupplier) resolveRegion(ctx context.Context) (string, error) {
	s.regionMu.Lock()
	defer s.regionMu.Unlock()
	if s.region != "" {
		return s.region, nil
	}
	switch {
	case s.regionOverride != "":
		s.region = s.regionOverride
	case s.sdkRegion != "":
		s.region = s.sdkRegion
	case s.imdsRegion != nil:
		region, err := s.imdsRegion(ctx)
		if err != nil || region == "" {
			return "", fmt.Errorf("vertex aws federation: aws region could not be determined (set aws_region in the key config or AWS_REGION in the environment): %v", err)
		}
		s.region = region
	default:
		return "", fmt.Errorf("vertex aws federation: aws region could not be determined (set aws_region in the key config or AWS_REGION in the environment)")
	}
	return s.region, nil
}

// awsSignedRequestHeader and awsSignedRequest mirror the JSON shape GCP STS expects as the
// subject token: the replayable request, URL-encoded.
type awsSignedRequestHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type awsSignedRequest struct {
	URL     string                   `json:"url"`
	Method  string                   `json:"method"`
	Headers []awsSignedRequestHeader `json:"headers"`
}

// SubjectToken signs an STS GetCallerIdentity request with the workload's AWS credentials and
// returns it serialized for the GCP token exchange.
func (s *awsSubjectTokenSupplier) SubjectToken(ctx context.Context, _ externalaccount.SupplierOptions) (string, error) {
	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("vertex aws federation: aws credentials: %w", err)
	}
	region, err := s.resolveRegion(ctx)
	if err != nil {
		return "", err
	}

	host := awsSTSHost(region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"?"+awsSTSGetCallerIdentityQuery, nil)
	if err != nil {
		return "", fmt.Errorf("vertex aws federation: sts signature: %w", err)
	}
	req.Header.Set(gcpTargetResourceHeader, s.audience)
	if err := s.signer.SignHTTP(ctx, creds, req, emptyPayloadSHA256, "sts", region, s.now()); err != nil {
		return "", fmt.Errorf("vertex aws federation: sts signature: %w", err)
	}

	signed := awsSignedRequest{URL: req.URL.String(), Method: http.MethodPost}
	signed.Headers = append(signed.Headers, awsSignedRequestHeader{Key: "host", Value: host})
	for name, values := range req.Header {
		for _, value := range values {
			signed.Headers = append(signed.Headers, awsSignedRequestHeader{Key: name, Value: value})
		}
	}
	sort.Slice(signed.Headers, func(i, j int) bool {
		if signed.Headers[i].Key != signed.Headers[j].Key {
			return signed.Headers[i].Key < signed.Headers[j].Key
		}
		return signed.Headers[i].Value < signed.Headers[j].Value
	})

	payload, err := providerUtils.MarshalSorted(signed)
	if err != nil {
		return "", fmt.Errorf("vertex aws federation: sts signature: %w", err)
	}
	return url.QueryEscape(string(payload)), nil
}

// isAWSExternalAccountJSON reports whether a Google credentials JSON is an external_account whose
// credential_source is AWS (environment_id "aws1"), the shape gcloud emits for
// `iam workload-identity-pools create-cred-config --aws`.
func isAWSExternalAccountJSON(jsonData []byte) bool {
	if gjson.GetBytes(jsonData, "type").String() != string(google.ExternalAccount) {
		return false
	}
	return strings.HasPrefix(gjson.GetBytes(jsonData, "credential_source.environment_id").String(), "aws")
}

// newAWSFederatedTokenSourceFromJSON builds the same federation flow as newAWSFederatedTokenSource
// from a gcloud-generated AWS external_account JSON. Google's library would otherwise resolve the AWS
// side itself from static env vars or instance metadata only; routing the JSON through Bifrost's
// supplier makes IRSA and Pod Identity work without changing the credential file.
func newAWSFederatedTokenSourceFromJSON(ctx context.Context, jsonData []byte) (oauth2.TokenSource, error) {
	audience := gjson.GetBytes(jsonData, "audience").String()
	if audience == "" {
		return nil, fmt.Errorf("vertex aws federation: external_account json is missing audience")
	}
	if env := gjson.GetBytes(jsonData, "credential_source.environment_id").String(); env != "aws1" {
		return nil, fmt.Errorf("vertex aws federation: unsupported aws credential source version %q", env)
	}
	lifetime, err := jsonImpersonationLifetimeSeconds(jsonData)
	if err != nil {
		return nil, err
	}
	supplier, err := newAWSSubjectTokenSupplier(ctx, &schemas.VertexAWSWorkloadIdentityConfig{Audience: *schemas.NewSecretVar(audience)})
	if err != nil {
		return nil, err
	}
	conf := externalaccount.Config{
		Audience:                       audience,
		SubjectTokenType:               gjson.GetBytes(jsonData, "subject_token_type").String(),
		TokenURL:                       gjson.GetBytes(jsonData, "token_url").String(),
		ServiceAccountImpersonationURL: gjson.GetBytes(jsonData, "service_account_impersonation_url").String(),
		ServiceAccountImpersonationLifetimeSeconds: lifetime,
		UniverseDomain:       gjson.GetBytes(jsonData, "universe_domain").String(),
		Scopes:               []string{cloudPlatformScope},
		SubjectTokenSupplier: supplier,
	}
	if conf.SubjectTokenType == "" {
		conf.SubjectTokenType = awsSubjectTokenType
	}
	if conf.TokenURL == "" {
		conf.TokenURL = gcpSTSTokenURL
	}
	ts, err := externalaccount.NewTokenSource(ctx, conf)
	if err != nil {
		return nil, fmt.Errorf("vertex aws federation: gcp token exchange setup: %w", err)
	}
	return ts, nil
}

// awsFederatedTokenSourceFromADCFile checks whether GOOGLE_APPLICATION_CREDENTIALS points at an AWS
// external_account JSON and, if so, builds the federated source for it. ok is false when the variable
// is unset, unreadable or names another credential type, so the caller falls back to the Google ADC
// lookup, which reports those cases in its own words.
func awsFederatedTokenSourceFromADCFile(ctx context.Context) (ts oauth2.TokenSource, ok bool, err error) {
	path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		return nil, false, nil
	}
	jsonData, readErr := os.ReadFile(path)
	if readErr != nil || !isAWSExternalAccountJSON(jsonData) {
		return nil, false, nil
	}
	ts, err = newAWSFederatedTokenSourceFromJSON(ctx, jsonData)
	if err != nil {
		return nil, false, err
	}
	return ts, true, nil
}

// jsonImpersonationLifetimeSeconds reads service_account_impersonation.token_lifetime_seconds from an
// external_account JSON strictly. gjson's Int() wraps an overflowing integer and truncates fractions
// or numeric strings (18446744073709555216 reads back as 3600), which would quietly request a
// different lifetime from IAM than the file states, so anything but a plain JSON integer that fits
// is a configuration error. An absent field keeps the library default.
func jsonImpersonationLifetimeSeconds(jsonData []byte) (int, error) {
	const field = "service_account_impersonation.token_lifetime_seconds"
	r := gjson.GetBytes(jsonData, field)
	if !r.Exists() {
		return 0, nil
	}
	if r.Type != gjson.Number {
		return 0, fmt.Errorf("vertex aws federation: %s must be a json integer, got %s", field, r.Raw)
	}
	n, err := strconv.ParseInt(r.Raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("vertex aws federation: %s must be a json integer that fits in 64 bits, got %s", field, r.Raw)
	}
	if int64(int(n)) != n {
		return 0, fmt.Errorf("vertex aws federation: %s is out of range: %s", field, r.Raw)
	}
	return int(n), nil
}
