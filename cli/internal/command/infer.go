package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/harness"
	"github.com/maximhq/bifrost/cli/internal/runtime"
)

// inferencePaths maps ergonomic operation names to canonical JSON endpoints.
var inferencePaths = map[string]string{
	"chat":                   "/v1/chat/completions",
	"completions":            "/v1/completions",
	"responses":              "/v1/responses",
	"embeddings":             "/v1/embeddings",
	"rerank":                 "/v1/rerank",
	"ocr":                    "/v1/ocr",
	"speech":                 "/v1/audio/speech",
	"transcription":          "/v1/audio/transcriptions",
	"image-generation":       "/v1/images/generations",
	"image-edit":             "/v1/images/edits",
	"image-variation":        "/v1/images/variations",
	"video-generation":       "/v1/videos",
	"video-edit":             "/v1/videos/edits",
	"count-tokens":           "/v1/responses/input_tokens",
	"compact":                "/v1/responses/compact",
	"mcp-tool":               "/v1/mcp/tool/execute",
	"async-chat":             "/v1/async/chat/completions",
	"async-completions":      "/v1/async/completions",
	"async-responses":        "/v1/async/responses",
	"async-embeddings":       "/v1/async/embeddings",
	"async-rerank":           "/v1/async/rerank",
	"async-ocr":              "/v1/async/ocr",
	"async-speech":           "/v1/async/audio/speech",
	"async-transcription":    "/v1/async/audio/transcriptions",
	"async-image-generation": "/v1/async/images/generations",
	"async-image-edit":       "/v1/async/images/edits",
	"async-image-variation":  "/v1/async/images/variations",
}

// runChat creates a basic OpenAI-compatible chat completion.
func (r *Runner) runChat(ctx context.Context, env *environment, args []string) error {
	if len(args) > 0 && args[0] == "completions" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, "Usage: bifrost chat [completions] [MODEL] [--message role:content ...] [--body JSON|--file PATH] [--stream] [flags]\n")
		return err
	}
	positionalModel := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positionalModel = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("bifrost chat", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var model string
	var messages repeatedValue
	var system string
	var bodyValue string
	var bodyFile string
	var stream bool
	var temperature float64
	var topP float64
	var count int
	var maxTokens int
	var presencePenalty float64
	var frequencyPenalty float64
	var user string
	fs.StringVar(&model, "model", defaultModel(env), "model identifier")
	fs.Var(&messages, "message", "message as role:content; repeatable")
	fs.Var(&messages, "m", "message as role:content; repeatable")
	fs.StringVar(&system, "system", "", "optional system message")
	fs.StringVar(&bodyValue, "body", "", "complete JSON request body")
	fs.StringVar(&bodyFile, "file", "", "complete JSON request body file, or - for stdin")
	fs.BoolVar(&stream, "stream", false, "request an SSE stream")
	fs.Float64Var(&temperature, "temperature", 0, "sampling temperature")
	fs.Float64Var(&temperature, "t", 0, "sampling temperature")
	fs.Float64Var(&topP, "top-p", 0, "nucleus sampling probability")
	fs.IntVar(&count, "n", 0, "number of completions")
	fs.IntVar(&maxTokens, "max-tokens", 0, "maximum output tokens")
	fs.Float64Var(&presencePenalty, "presence-penalty", 0, "presence penalty")
	fs.Float64Var(&frequencyPenalty, "frequency-penalty", 0, "frequency penalty")
	fs.StringVar(&user, "user", "", "end-user identifier")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected chat arguments: %s", strings.Join(fs.Args(), " "))
	}
	if positionalModel != "" {
		if visitedFlag(fs, "model") {
			return fmt.Errorf("model may be supplied positionally or with --model, not both")
		}
		model = positionalModel
	}
	body, err := r.readBody(bodyValue, bodyFile)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		if strings.TrimSpace(model) == "" || len(messages) == 0 {
			return fmt.Errorf("a model and at least one --message are required when a complete body is not supplied")
		}
		requestMessages := []map[string]string{}
		if strings.TrimSpace(system) != "" {
			requestMessages = append(requestMessages, map[string]string{"role": "system", "content": system})
		}
		for _, message := range messages {
			role, content := "user", message
			if parsedRole, parsedContent, ok := strings.Cut(message, ":"); ok && strings.TrimSpace(parsedRole) != "" {
				role, content = strings.TrimSpace(parsedRole), parsedContent
			}
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("chat messages cannot be empty")
			}
			requestMessages = append(requestMessages, map[string]string{"role": role, "content": content})
		}
		payload := map[string]any{"model": model, "messages": requestMessages, "stream": stream}
		setChatOptionalParameters(fs, payload, temperature, topP, count, maxTokens, presencePenalty, frequencyPenalty, user)
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	} else if !json.Valid(body) {
		return fmt.Errorf("request body must be valid JSON")
	} else if !stream {
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil {
			stream, _ = payload["stream"].(bool)
		}
	}
	request := client.Request{Method: http.MethodPost, Path: inferencePaths["chat"], Body: body, Auth: client.AuthInference}
	if stream {
		return env.Client.Stream(ctx, request, r.Out)
	}
	return r.executeAndPrint(ctx, env, request)
}

// visitedFlag reports whether a specific flag was explicitly supplied.
func visitedFlag(fs *flag.FlagSet, name string) bool {
	visited := false
	fs.Visit(func(item *flag.Flag) {
		if item.Name == name {
			visited = true
		}
	})
	return visited
}

// setChatOptionalParameters copies only explicitly supplied generation controls.
func setChatOptionalParameters(fs *flag.FlagSet, payload map[string]any, temperature, topP float64, count, maxTokens int, presencePenalty, frequencyPenalty float64, user string) {
	if visitedFlag(fs, "temperature") || visitedFlag(fs, "t") {
		payload["temperature"] = temperature
	}
	if visitedFlag(fs, "top-p") {
		payload["top_p"] = topP
	}
	if visitedFlag(fs, "n") {
		payload["n"] = count
	}
	if visitedFlag(fs, "max-tokens") {
		payload["max_tokens"] = maxTokens
	}
	if visitedFlag(fs, "presence-penalty") {
		payload["presence_penalty"] = presencePenalty
	}
	if visitedFlag(fs, "frequency-penalty") {
		payload["frequency_penalty"] = frequencyPenalty
	}
	if visitedFlag(fs, "user") {
		payload["user"] = user
	}
}

// runInfer invokes any canonical JSON inference endpoint with a complete body.
func (r *Runner) runInfer(ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		operations := make([]string, 0, len(inferencePaths))
		for operation := range inferencePaths {
			operations = append(operations, operation)
		}
		sort.Strings(operations)
		_, err := fmt.Fprintf(r.Out, "Usage: bifrost infer <operation> --body JSON|--file PATH [--header 'Name: value'] [--stream]\n\nOperations:\n  %s\n", strings.Join(operations, "\n  "))
		return err
	}
	operation := args[0]
	path, ok := inferencePaths[operation]
	if !ok {
		return fmt.Errorf("unknown inference operation %q", operation)
	}
	if operation == "chat" {
		return r.runChat(ctx, env, args[1:])
	}
	fs := flag.NewFlagSet("bifrost infer "+operation, flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var bodyValue string
	var bodyFile string
	var headerFlags repeatedValue
	var stream bool
	fs.StringVar(&bodyValue, "body", "", "inline JSON request body")
	fs.StringVar(&bodyFile, "file", "", "request body file, or - for stdin")
	fs.Var(&headerFlags, "header", "request header as Name: value; repeatable")
	fs.BoolVar(&stream, "stream", false, "copy a streaming response incrementally")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected infer arguments: %s", strings.Join(fs.Args(), " "))
	}
	body, err := r.readBody(bodyValue, bodyFile)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("infer %s requires --body or --file", operation)
	}
	headers, err := parseHeaders(headerFlags)
	if err != nil {
		return err
	}
	contentType := strings.ToLower(strings.TrimSpace(headers.Get("Content-Type")))
	if (contentType == "" || strings.Contains(contentType, "json")) && !json.Valid(body) {
		return fmt.Errorf("request body must be valid JSON")
	}
	request := client.Request{Method: http.MethodPost, Path: path, Headers: headers, Body: body, Auth: client.AuthInference}
	if stream {
		return env.Client.Stream(ctx, request, r.Out)
	}
	return r.executeAndPrint(ctx, env, request)
}

// runHarness launches one coding agent directly with argument passthrough.
func (r *Runner) runHarness(ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, "Usage: bifrost run <claude|codex|gemini|opencode> [--model MODEL] [--worktree NAME] [--skip-verify] [-- agent arguments]\n")
		return err
	}
	agent, ok := harness.Get(args[0])
	if !ok {
		return fmt.Errorf("unknown coding agent %q", args[0])
	}
	fs := flag.NewFlagSet("bifrost run "+agent.ID, flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var model string
	var worktree string
	var skipVerify bool
	fs.StringVar(&model, "model", defaultModel(env), "model identifier")
	fs.StringVar(&worktree, "worktree", "", "create a worktree when supported")
	fs.BoolVar(&skipVerify, "skip-verify", false, "skip binary and gateway preflight")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !skipVerify {
		if _, err := exec.LookPath(agent.Binary); err != nil {
			return fmt.Errorf("%s is not installed or not in PATH", agent.Label)
		}
		if _, err := env.Client.Do(ctx, client.Request{Path: "/v1/models", Auth: client.AuthInference}); err != nil {
			return fmt.Errorf("gateway preflight failed: %w", err)
		}
	}
	credentials := env.Client.CredentialsSnapshot()
	agentToken := strings.TrimSpace(credentials.AgentToken)
	tokenHelperCommand, err := runtime.PrepareLaunchAuthentication(agent, agentToken, credentials.VirtualKey)
	if err != nil {
		return err
	}
	return runtime.RunInteractive(ctx, r.Out, r.ErrOut, runtime.LaunchSpec{
		Harness: agent, BaseURL: env.Client.BaseURL, VirtualKey: env.VirtualKey, AgentToken: agentToken,
		Context: env.ProfileID, StatePath: env.StatePath, TokenHelperCommand: tokenHelperCommand,
		Model: strings.TrimSpace(model), Worktree: strings.TrimSpace(worktree), Args: fs.Args(),
	})
}

// defaultModel resolves environment, profile, and launcher selection model defaults.
func defaultModel(env *environment) string {
	if model := strings.TrimSpace(environmentDefault("BIFROST_MODEL", "")); model != "" {
		return model
	}
	if env.Profile != nil {
		if model := strings.TrimSpace(env.Profile.DefaultModel); model != "" {
			return model
		}
		if selection, ok := env.State.Selections[env.Profile.ID]; ok {
			return strings.TrimSpace(selection.Model)
		}
	}
	return ""
}
