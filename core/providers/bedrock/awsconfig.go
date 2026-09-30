package bedrock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// resolveAWSConfig keeps credential selection identical across Bedrock signing
// and S3 uploads. A selected profile never falls through to ambient credentials.
func resolveAWSConfig(
	ctx context.Context,
	accessKey, secretKey schemas.SecretVar,
	sessionToken, profile, roleARN, externalID, sessionName *schemas.SecretVar,
	region string,
) (aws.Config, *schemas.BifrostError) {
	var profileName string
	if profile != nil {
		profileName = strings.TrimSpace(profile.GetValue())
		if profile.IsFromEnv() {
			profileName = strings.TrimSpace(os.Getenv(profile.EnvKey()))
		}
		if profileName == "" {
			return aws.Config{}, providerUtils.NewBifrostOperationError(
				"configured bedrock profile resolved to an empty value",
				errors.New("run aws sso login after configuring a non-empty profile"),
			)
		}
		if accessKey.IsSet() || secretKey.IsSet() || (sessionToken != nil && sessionToken.IsSet()) {
			return aws.Config{}, providerUtils.NewBifrostOperationError(
				"bedrock profile cannot be combined with explicit credentials",
				errors.New("choose one credential source"),
			)
		}
	}

	accessKeyValue := accessKey.GetValue()
	secretKeyValue := secretKey.GetValue()
	staticConfigured := accessKey.IsSet() || secretKey.IsSet()
	if staticConfigured && (accessKeyValue == "" || secretKeyValue == "") {
		return aws.Config{}, providerUtils.NewBifrostOperationError(
			"bedrock explicit credentials are incomplete",
			errors.New("both access key and secret key are required"),
		)
	}

	var (
		cfg aws.Config
		err error
	)
	switch {
	case profileName != "":
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithSharedConfigProfile(profileName),
		)
	case staticConfigured:
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				creds := aws.Credentials{
					AccessKeyID:     accessKeyValue,
					SecretAccessKey: secretKeyValue,
				}
				if sessionToken != nil {
					creds.SessionToken = sessionToken.GetValue()
				}
				return creds, nil
			})),
		)
	default:
		cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	}
	if err != nil {
		return aws.Config{}, providerUtils.NewBifrostOperationError("failed to load aws config", err)
	}

	if roleARN != nil && roleARN.GetValue() != "" {
		extID := ""
		if externalID != nil {
			extID = externalID.GetValue()
		}
		sessName := "bifrost-session"
		if sessionName != nil && sessionName.GetValue() != "" {
			sessName = sessionName.GetValue()
		}
		sourceIdentity := "default_chain"
		if staticConfigured {
			sourceIdentity = accessKeyValue
			if sessionToken != nil && sessionToken.GetValue() != "" {
				tokenHash := sha256.Sum256([]byte(sessionToken.GetValue()))
				sourceIdentity += "|" + hex.EncodeToString(tokenHash[:8])
			}
		} else if profileName != "" {
			sourceIdentity = "profile:" + profileName
		}
		cacheKey := strings.Join([]string{
			region,
			roleARN.GetValue(),
			extID,
			sessName,
			sourceIdentity,
		}, "|")

		if cached, ok := assumeRoleCredsCache.Load(cacheKey); ok {
			cfg.Credentials = cached.(*aws.CredentialsCache)
		} else {
			stsClient := sts.NewFromConfig(cfg)
			opts := func(o *stscreds.AssumeRoleOptions) {
				if extID != "" {
					o.ExternalID = aws.String(extID)
				}
				o.RoleSessionName = sessName
			}
			credsCache := aws.NewCredentialsCache(
				stscreds.NewAssumeRoleProvider(stsClient, roleARN.GetValue(), opts),
			)
			actual, _ := assumeRoleCredsCache.LoadOrStore(cacheKey, credsCache)
			cfg.Credentials = actual.(*aws.CredentialsCache)
		}
	}

	return cfg, nil
}

func bedrockCredentialError(err error) *schemas.BifrostError {
	var invalidSSOToken *ssocreds.InvalidTokenError
	if errors.As(err, &invalidSSOToken) {
		return providerUtils.NewBifrostOperationError(
			"aws sso session expired or invalid; run aws sso login --profile <name> on the gateway host",
			err,
		)
	}
	return providerUtils.NewBifrostOperationError("failed to retrieve aws credentials", err)
}
