package tokencache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	tokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange"
	// A projected service account token is about a kilobyte.
	maxSubjectTokenBytes = 64 * 1024
)

// TokenExchangeConfig configures one RFC 8693 exchange of a workload's subject token.
type TokenExchangeConfig struct {
	TokenURL         string                       // Token endpoint
	SubjectTokenFile string                       // File holding the subject token, re-read on every exchange
	SubjectTokenType string                       // RFC 8693 token type URI (default schemas.OAuthTokenTypeJWT)
	Audience         string                       // Optional audience parameter
	Scopes           []string                     // Scopes requested, sent space-joined
	Encoding         schemas.OAuthRequestEncoding // form (default) or json
	ExtraParams      map[string]string            // Extra parameters, e.g. identity_provider_id
	RefreshMargin    time.Duration                // How long before expiry to re-mint (default DefaultRefreshMargin)
}

// TokenExchangeMinter re-reads the subject token file on every mint: platforms rotate it in place.
func TokenExchangeMinter(cfg TokenExchangeConfig, hc *http.Client) Minter[string] {
	margin := cfg.RefreshMargin
	if margin <= 0 {
		margin = DefaultRefreshMargin
	}
	tokenType := cfg.SubjectTokenType
	if tokenType == "" {
		tokenType = schemas.OAuthTokenTypeJWT
	}
	hc = guardRedirects(exchangeClient(hc))
	return func(ctx context.Context, _ *Entry[string]) (*Entry[string], *schemas.BifrostError) {
		subjectToken, err := readSubjectToken(cfg.SubjectTokenFile)
		if err != nil {
			return nil, configurationError("oauth token_exchange: " + err.Error())
		}
		params := make(map[string]string, len(cfg.ExtraParams)+5)
		for k, v := range cfg.ExtraParams {
			params[k] = v
		}
		params["grant_type"] = tokenExchangeGrantType
		params["subject_token"] = subjectToken
		params["subject_token_type"] = tokenType
		if cfg.Audience != "" {
			params["audience"] = cfg.Audience
		}
		if len(cfg.Scopes) > 0 {
			params["scope"] = strings.Join(cfg.Scopes, " ")
		}
		// Everything but the fixed protocol values may be a credential.
		var submitted []string
		for k, v := range params {
			if k != "grant_type" && k != "subject_token_type" && k != "scope" {
				submitted = append(submitted, v)
			}
		}

		contentType := "application/x-www-form-urlencoded"
		var body []byte
		if cfg.Encoding == schemas.OAuthRequestEncodingJSON {
			contentType = "application/json"
			if body, err = sonic.Marshal(params); err != nil {
				return nil, configurationError("oauth token_exchange request could not be encoded: " + err.Error())
			}
		} else {
			form := make(url.Values, len(params))
			for k, v := range params {
				form.Set(k, v)
			}
			body = []byte(form.Encode())
		}
		token, expiry, bErr := postToken(ctx, hc, cfg.TokenURL, body, contentType, submitted)
		if bErr != nil {
			return nil, bErr
		}
		return entryFor(token, expiry, margin), nil
	}
}

func readSubjectToken(path string) (string, error) {
	real, err := schemas.ResolveSubjectTokenFile(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(real)
	if err != nil {
		return "", fmt.Errorf("subject_token_file could not be read: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSubjectTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("subject_token_file could not be read: %w", err)
	}
	if len(data) > maxSubjectTokenBytes {
		return "", fmt.Errorf("subject_token_file is larger than %d bytes", maxSubjectTokenBytes)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("subject_token_file is empty")
	}
	return token, nil
}
