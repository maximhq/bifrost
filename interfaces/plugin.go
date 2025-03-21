package interfaces

type RequestInput struct {
	StringInput  *string
	MessageInput *[]Message
}

type BifrostRequest struct {
	Model        string
	Input        RequestInput
	Params       *ModelParameters
	PluginParams map[string]interface{}
}

type Plugin interface {
	PreHook(req *BifrostRequest) (*BifrostRequest, error)
	PostHook(result *CompletionResult) (*CompletionResult, error)
}
