package opencode

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type adapterKind string

const (
	adapterOpenAIResponses   adapterKind = "openai_responses"
	adapterOpenAIChat        adapterKind = "openai_chat"
	adapterAnthropicMessages adapterKind = "anthropic_messages"
	adapterGeminiNative      adapterKind = "gemini_native"
)

type authStyle string

const (
	authStyleBearer       authStyle = "bearer"
	authStyleAnthropicKey authStyle = "anthropic_key"
	authStyleGeminiGateway authStyle = "gemini_gateway"
)

type routeMatchKind string

const (
	routeMatchExact   routeMatchKind = "exact"
	routeMatchClass   routeMatchKind = "class"
	routeMatchDefault routeMatchKind = "default"
)

type resolvedRoute struct {
	Adapter     adapterKind
	Path        string
	Auth        authStyle
	MatchedBy   routeMatchKind
	ClassPrefix string
}

type routeSpec struct {
	adapter adapterKind
	path    string
	auth    authStyle
}

type classRoute struct {
	prefix string
	spec   routeSpec
}

var zenExactRoutes = map[string]routeSpec{
	"gpt-5.5":               {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.5-pro":           {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.4":               {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.4-pro":           {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.4-mini":          {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.4-nano":          {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.3-codex":         {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.3-codex-spark":   {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.2":               {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.2-codex":         {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.1":               {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.1-codex":         {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.1-codex-max":     {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5.1-codex-mini":    {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5":                 {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5-codex":           {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"gpt-5-nano":            {adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer},
	"claude-fable-5":        {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-opus-4-8":       {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-opus-4-7":       {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-opus-4-6":       {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-opus-4-5":       {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-opus-4-1":       {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-sonnet-4-6":     {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-sonnet-4-5":     {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-sonnet-4":       {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-haiku-4-5":      {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"claude-3-5-haiku":      {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.7-max":           {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.7-plus":          {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.6-plus":          {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.5-plus":          {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"deepseek-v4-pro":       {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"deepseek-v4-flash":     {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"minimax-m2.7":          {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"minimax-m2.5":          {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"glm-5.1":               {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"glm-5":                 {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"kimi-k2.5":             {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"kimi-k2.6":             {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"grok-build-0.1":        {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"big-pickle":            {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"mimo-v2.5-free":        {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"north-mini-code-free":  {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"nemotron-3-ultra-free": {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"deepseek-v4-flash-free": {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"gemini-3.5-flash":      {adapter: adapterGeminiNative, path: "/v1/models/{model}", auth: authStyleGeminiGateway},
	"gemini-3.1-pro":        {adapter: adapterGeminiNative, path: "/v1/models/{model}", auth: authStyleGeminiGateway},
	"gemini-3-flash":        {adapter: adapterGeminiNative, path: "/v1/models/{model}", auth: authStyleGeminiGateway},
}

var goExactRoutes = map[string]routeSpec{
	"glm-5.1":         {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"glm-5":           {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"kimi-k2.7":       {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"kimi-k2.6":       {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"deepseek-v4-pro": {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"deepseek-v4-flash": {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"mimo-v2.5":       {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"mimo-v2.5-pro":   {adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer},
	"minimax-m3":      {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"minimax-m2.7":    {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"minimax-m2.5":    {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.7-max":     {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.7-plus":    {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
	"qwen3.6-plus":    {adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey},
}

var zenClassRoutes = []classRoute{
	{prefix: "gpt-", spec: routeSpec{adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer}},
	{prefix: "claude-", spec: routeSpec{adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey}},
	{prefix: "qwen", spec: routeSpec{adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey}},
	{prefix: "gemini-", spec: routeSpec{adapter: adapterGeminiNative, path: "/v1/models/{model}", auth: authStyleGeminiGateway}},
	{prefix: "deepseek-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "minimax-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "glm-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "kimi-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "grok-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "mimo-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
}

var goClassRoutes = []classRoute{
	{prefix: "gpt-", spec: routeSpec{adapter: adapterOpenAIResponses, path: "/v1/responses", auth: authStyleBearer}},
	{prefix: "claude-", spec: routeSpec{adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey}},
	{prefix: "gemini-", spec: routeSpec{adapter: adapterGeminiNative, path: "/v1/models/{model}", auth: authStyleGeminiGateway}},
	{prefix: "qwen", spec: routeSpec{adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey}},
	{prefix: "minimax-", spec: routeSpec{adapter: adapterAnthropicMessages, path: "/v1/messages", auth: authStyleAnthropicKey}},
	{prefix: "glm-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "kimi-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "deepseek-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
	{prefix: "mimo-", spec: routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}},
}

func resolveRoute(providerKey schemas.ModelProvider, modelID string) resolvedRoute {
	normalizedModel := strings.TrimSpace(strings.ToLower(modelID))
	if spec, ok := resolveExactRoute(providerKey, normalizedModel); ok {
		return buildResolvedRoute(spec, routeMatchExact, "", normalizedModel)
	}
	if spec, prefix, ok := resolveClassRoute(providerKey, normalizedModel); ok {
		return buildResolvedRoute(spec, routeMatchClass, prefix, normalizedModel)
	}
	return buildResolvedRoute(defaultChatRoute(providerKey), routeMatchDefault, "", normalizedModel)
}

func resolveExactRoute(providerKey schemas.ModelProvider, modelID string) (routeSpec, bool) {
	switch providerKey {
	case schemas.OpencodeZen:
		spec, ok := zenExactRoutes[modelID]
		return spec, ok
	case schemas.OpencodeGo:
		spec, ok := goExactRoutes[modelID]
		return spec, ok
	default:
		return routeSpec{}, false
	}
}

func resolveClassRoute(providerKey schemas.ModelProvider, modelID string) (routeSpec, string, bool) {
	var routes []classRoute
	switch providerKey {
	case schemas.OpencodeZen:
		routes = zenClassRoutes
	case schemas.OpencodeGo:
		routes = goClassRoutes
	default:
		return routeSpec{}, "", false
	}

	for _, route := range routes {
		if strings.HasPrefix(modelID, route.prefix) {
			return route.spec, route.prefix, true
		}
	}

	return routeSpec{}, "", false
}

func defaultChatRoute(providerKey schemas.ModelProvider) routeSpec {
	return routeSpec{adapter: adapterOpenAIChat, path: "/v1/chat/completions", auth: authStyleBearer}
}

func buildResolvedRoute(spec routeSpec, matchedBy routeMatchKind, classPrefix, modelID string) resolvedRoute {
	path := spec.path
	if strings.Contains(path, "{model}") {
		path = strings.ReplaceAll(path, "{model}", modelID)
	}
	return resolvedRoute{
		Adapter:     spec.adapter,
		Path:        path,
		Auth:        spec.auth,
		MatchedBy:   matchedBy,
		ClassPrefix: classPrefix,
	}
}
