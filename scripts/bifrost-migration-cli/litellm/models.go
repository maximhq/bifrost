package litellm

import (
	"context"
	"fmt"
)

type LiteLLMModelInfo struct {
	ModelName     string                         `json:"model_name"`
	LiteLLMParams *LiteLLMModelInfoLiteLLMParams `json:"litellm_params"`
	ModelInfo     *LiteLLMModelInfoParams        `json:"model_info"`
}

type LiteLLMModelInfoLiteLLMParams struct {
	UseInPassThrough               bool    `json:"use_in_pass_through"`
	UseLitellmProxy                bool    `json:"use_litellm_proxy"`
	MergeReasoningContentInChoices bool    `json:"merge_reasoning_content_in_choices"`
	Model                          string  `json:"model"`
	CustomLLMProvider              string  `json:"custom_llm_provider"`
	LiteLLMCredentialName          *string `json:"litellm_credential_name"`
	Tpm                            *int64  `json:"tpm"`
	Rpm                            *int64  `json:"rpm"`
}

type LiteLLMModelInfoParams struct {
	Id              string `json:"id"`
	DbModel         bool   `json:"db_model"`
	Key             string `json:"key"`
	MaxTokens       *int   `json:"max_tokens"`
	MaxInputTokens  *int   `json:"max_input_tokens"`
	MaxOutputTokens *int   `json:"max_output_tokens"`
	Tpm             *int64 `json:"tpm"`
	Rpm             *int64 `json:"rpm"`
}

type LiteLLMModelCredential struct {
	CredentialName   string                        `json:"credential_name"`
	CredentialValues *LiteLLMModelCredentialValues `json:"credential_values"`
	CredentialInfo   *LiteLLMModelCredentialInfo   `json:"credential_info"`
}

type LiteLLMModelCredentialValues struct {
	ApiKey  *string `json:"api_key"`
	ApiBase *string `json:"api_base"`
}

type LiteLLMModelCredentialInfo struct {
	CustomLLMProvider string `json:"custom_llm_provider"`
}

// ListModelInfo fetches every deployment via GET /model/info
func (c *LiteLLMClient) ListModelInfo(ctx context.Context) ([]LiteLLMModelInfo, error) {
	var out struct {
		Data []LiteLLMModelInfo `json:"data"`
	}

	if err := doGet(ctx, c, "/model/info", &out); err != nil {
		return nil, fmt.Errorf("list model info: %w", err)
	}
	return out.Data, nil
}

// ListCredentials fetches every stored credential via GET /credentials
func (c *LiteLLMClient) ListCredentials(ctx context.Context) ([]LiteLLMModelCredential, error) {
	var out struct {
		Credentials []LiteLLMModelCredential `json:"credentials"`
	}

	if err := doGet(ctx, c, "/credentials", &out); err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return out.Credentials, nil
}
