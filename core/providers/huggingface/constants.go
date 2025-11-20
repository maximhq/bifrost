package huggingface

const (
	// According to https://huggingface.co/docs/inference-providers/en/tasks/chat-completion the
	// OpenAI-compatible router lives under the /v1 prefix, so we wire that in as the default base URL.
	defaultInferenceBaseURL = "https://router.huggingface.co/v1"
	modelHubBaseURL         = "https://huggingface.co"
)

// List of supported inference providers (kept in sync with HF docs/JS SDK)
var INFERENCE_PROVIDERS = []string{
	"baseten",
	"black-forest-labs",
	"cerebras",
	"clarifai",
	"cohere",
	"fal-ai",
	"featherless-ai",
	"fireworks-ai",
	"groq",
	"hf-inference",
	"hyperbolic",
	"nebius",
	"novita",
	"nscale",
	"openai",
	"ovhcloud",
	"publicai",
	"replicate",
	"sambanova",
	"scaleway",
	"together",
	"wavespeed",
	"zai-org",
}

// PROVIDERS_OR_POLICIES is the above list plus the special "auto" policy
var PROVIDERS_OR_POLICIES = func() []string {
	out := make([]string, 0, len(INFERENCE_PROVIDERS)+1)
	out = append(out, INFERENCE_PROVIDERS...)
	out = append(out, "auto")
	return out
}()

// PROVIDERS_HUB_ORGS maps an inference provider to the expected HF Hub org namespace
var PROVIDERS_HUB_ORGS = map[string]string{
	"baseten":           "baseten",
	"black-forest-labs": "black-forest-labs",
	"cerebras":          "cerebras",
	"clarifai":          "clarifai",
	"cohere":            "CohereLabs",
	"fal-ai":            "fal",
	"featherless-ai":    "featherless-ai",
	"fireworks-ai":      "fireworks-ai",
	"groq":              "groq",
	"hf-inference":      "hf-inference",
	"hyperbolic":        "Hyperbolic",
	"nebius":            "nebius",
	"novita":            "novita",
	"nscale":            "nscale",
	"openai":            "openai",
	"ovhcloud":          "ovhcloud",
	"publicai":          "publicai",
	"replicate":         "replicate",
	"sambanova":         "sambanovasystems",
	"scaleway":          "scaleway",
	"together":          "togethercomputer",
	"wavespeed":         "wavespeed",
	"zai-org":           "zai-org",
}
