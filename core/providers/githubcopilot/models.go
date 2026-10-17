package githubcopilot

import (
	"github.com/maximhq/bifrost/core/providers/openai"
)

const (
	copilotPolicyDisabled = "disabled"
	copilotModelTypeChat  = "chat"
)

// copilotModel is one row of Copilot's /models response. It extends the OpenAI shape with
// the fields that say whether this provider can serve the model.
type copilotModel struct {
	openai.OpenAIModel
	Policy *struct {
		State string `json:"state"`
	} `json:"policy,omitempty"`
	Capabilities *struct {
		Type string `json:"type"`
	} `json:"capabilities,omitempty"`
}

// copilotListModelsResponse is Copilot's /models response.
type copilotListModelsResponse struct {
	Object string         `json:"object"`
	Data   []copilotModel `json:"data"`
}

// usable reports whether this provider can serve the model. Copilot lists models that the
// account's policy disables, and embedding and completion models that this provider has no
// operation for.
func (m copilotModel) usable() bool {
	if m.Policy != nil && m.Policy.State == copilotPolicyDisabled {
		return false
	}
	if m.Capabilities != nil && m.Capabilities.Type != "" && m.Capabilities.Type != copilotModelTypeChat {
		return false
	}
	return true
}

// toOpenAIListModelsResponse keeps the usable models and returns them in the OpenAI shape.
func (response *copilotListModelsResponse) toOpenAIListModelsResponse() *openai.OpenAIListModelsResponse {
	models := make([]openai.OpenAIModel, 0, len(response.Data))
	for _, model := range response.Data {
		if model.usable() {
			models = append(models, model.OpenAIModel)
		}
	}
	return &openai.OpenAIListModelsResponse{Object: response.Object, Data: models}
}
