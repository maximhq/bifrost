package codex

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

const (
	OAuthClientID            = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthIssuer              = "https://auth.openai.com"
	DeviceVerificationURL    = OAuthIssuer + "/codex/device"
	deviceCallbackRedirect   = OAuthIssuer + "/deviceauth/callback"
	defaultPollingMarginSecs = 3
)

type TokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type DeviceAuthorizationResponse struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	Interval     string `json:"interval"`
}

type DeviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type IDTokenClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	Organizations    []struct {
		ID string `json:"id"`
	} `json:"organizations,omitempty"`
	OpenAIAuth *struct {
		ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	} `json:"https://api.openai.com/auth,omitempty"`
}

func RefreshAccessToken(ctx context.Context, client *http.Client, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", OAuthClientID)
	return executeTokenRequest(ctx, client, OAuthIssuer+"/oauth/token", strings.NewReader(form.Encode()))
}

func StartDeviceAuthorization(ctx context.Context, client *http.Client, userAgent string) (*DeviceAuthorizationResponse, error) {
	requestBody, err := sonic.Marshal(map[string]string{"client_id": OAuthClientID})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthIssuer+"/api/accounts/deviceauth/usercode", strings.NewReader(string(requestBody)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed with status %d", response.StatusCode)
	}
	var result DeviceAuthorizationResponse
	if err := sonic.ConfigDefault.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func PollDeviceAuthorization(ctx context.Context, client *http.Client, deviceAuthID, userCode, userAgent string) (*DeviceTokenResponse, int, error) {
	requestBody, err := sonic.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})
	if err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthIssuer+"/api/accounts/deviceauth/token", strings.NewReader(string(requestBody)))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, nil
	}
	var result DeviceTokenResponse
	if err := sonic.ConfigDefault.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, response.StatusCode, err
	}
	return &result, response.StatusCode, nil
}

func ExchangeDeviceAuthorizationCode(ctx context.Context, client *http.Client, code, codeVerifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", deviceCallbackRedirect)
	form.Set("client_id", OAuthClientID)
	form.Set("code_verifier", codeVerifier)
	return executeTokenRequest(ctx, client, OAuthIssuer+"/oauth/token", strings.NewReader(form.Encode()))
}

func ExtractAccountID(tokens *TokenResponse) string {
	if tokens == nil {
		return ""
	}
	for _, candidate := range []string{tokens.IDToken, tokens.AccessToken} {
		claims := parseJWTClaims(candidate)
		if claims == nil {
			continue
		}
		if claims.ChatGPTAccountID != "" {
			return claims.ChatGPTAccountID
		}
		if claims.OpenAIAuth != nil && claims.OpenAIAuth.ChatGPTAccountID != "" {
			return claims.OpenAIAuth.ChatGPTAccountID
		}
		if len(claims.Organizations) > 0 && claims.Organizations[0].ID != "" {
			return claims.Organizations[0].ID
		}
	}
	return ""
}

func ExpiresAtFromNow(expiresIn int) string {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
}

func NextPollTime(intervalSeconds int) time.Time {
	if intervalSeconds <= 0 {
		intervalSeconds = 5
	}
	return time.Now().Add(time.Duration(intervalSeconds+defaultPollingMarginSecs) * time.Second)
}

func executeTokenRequest(ctx context.Context, client *http.Client, endpoint string, body *strings.Reader) (*TokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed with status %d", response.StatusCode)
	}
	var tokenResponse TokenResponse
	if err := sonic.ConfigDefault.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
		return nil, err
	}
	return &tokenResponse, nil
}

func generateRandomString(length int) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	result := make([]byte, length)
	for i, value := range bytes {
		result[i] = chars[int(value)%len(chars)]
	}
	return string(result), nil
}

func parseJWTClaims(token string) *IDTokenClaims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims IDTokenClaims
	if err := sonic.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return &claims
}
