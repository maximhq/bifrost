package openai

import (
	"encoding/base64"
	"fmt"
	"testing"
)

// makeTestJWT builds a syntactically valid JWT with arbitrary header/payload JSON.
// The signature segment is a fixed placeholder — ParseChatGPTJWT never verifies it.
func makeTestJWT(payloadJSON string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return fmt.Sprintf("%s.%s.fakesig", header, payload)
}

func TestParseChatGPTJWT(t *testing.T) {
	validAccountID := "9dce4683-94cd-4aeb-ade4-4ecce82ebac5"

	tests := []struct {
		name      string
		token     string
		wantID    string
		wantOK    bool
	}{
		{
			name: "valid ChatGPT JWT returns account ID",
			token: makeTestJWT(fmt.Sprintf(
				`{"aud":["https://api.openai.com/v1"],"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`,
				validAccountID,
			)),
			wantID: validAccountID,
			wantOK: true,
		},
		{
			name: "JWT missing chatgpt_account_id claim returns false",
			token: makeTestJWT(`{"aud":["https://api.openai.com/v1"],"sub":"user-abc"}`),
			wantID: "",
			wantOK: false,
		},
		{
			name: "JWT with https://api.openai.com/auth but no chatgpt_account_id returns false",
			token: makeTestJWT(`{"https://api.openai.com/auth":{"other_field":"value"}}`),
			wantID: "",
			wantOK: false,
		},
		{
			name:   "not a JWT (sk- API key) returns false",
			token:  "sk-proj-abcdefghijklmnopqrstuvwxyz",
			wantID: "",
			wantOK: false,
		},
		{
			name:   "empty string returns false",
			token:  "",
			wantID: "",
			wantOK: false,
		},
		{
			name:   "only two segments returns false",
			token:  "header.payload",
			wantID: "",
			wantOK: false,
		},
		{
			name:   "invalid base64 in payload returns false",
			token:  "header.!!!invalid!!!.sig",
			wantID: "",
			wantOK: false,
		},
		{
			name:   "payload is valid base64 but not JSON returns false",
			token:  fmt.Sprintf("header.%s.sig", base64.RawURLEncoding.EncodeToString([]byte("not-json"))),
			wantID: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotOK := ParseChatGPTJWT(tt.token)
			if gotOK != tt.wantOK {
				t.Errorf("ParseChatGPTJWT() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotID != tt.wantID {
				t.Errorf("ParseChatGPTJWT() accountID = %q, want %q", gotID, tt.wantID)
			}
		})
	}
}
