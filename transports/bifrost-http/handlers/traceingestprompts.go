package handlers

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TracePromptStore is the part of the prompt repository the SDK prompt routes read.
type TracePromptStore interface {
	GetPrompts(ctx context.Context, folderID *string) ([]tables.TablePrompt, error)
	GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error)
	GetPromptVersionByID(ctx context.Context, id uint) (*tables.TablePromptVersion, error)
}

// tracePromptIndex is the metadata-only reads a prompt store can offer. With them a fetch reads
// the prompt names, one prompt's version list and one version's messages, instead of every
// prompt's latest version and every version's messages. A store without them is read the long way.
type tracePromptIndex interface {
	ListPromptSummaries(ctx context.Context) ([]tables.TablePrompt, error)
	ListPromptVersionSummaries(ctx context.Context, promptIDs []string) ([]tables.TablePromptVersion, error)
}

// errPromptAmbiguous is returned when a reference is no prompt's exact name and is the slug of more
// than one.
var errPromptAmbiguous = errors.New("more than one prompt has this slug: ask for the prompt by its exact name")

// SetPromptStore gives the ingest handler the prompt repository, so SDK prompt fetches are served
// from it. Without one, the prompt routes answer 404.
func (h *TraceIngestHandler) SetPromptStore(store TracePromptStore) {
	h.prompts = store
}

func init() {
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		chain := func(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
			return lib.ChainMiddlewares(handler, middlewares...)
		}
		// Langfuse: get_prompt() and the prompt list.
		rt.GET(langfusePrefix+"/api/public/v2/prompts", chain(h.langfuseListPrompts))
		rt.GET(langfusePrefix+"/api/public/v2/prompts/{name}", chain(h.langfuseGetPrompt))
		rt.POST(langfusePrefix+"/api/public/v2/prompts", chain(h.promptWriteRefused))
		// Braintrust: load_prompt().
		rt.GET(braintrustPrefix+"/v1/prompt", chain(h.braintrustGetPrompt))
	})
}

// pathUnescape decodes a path segment an SDK escaped: a prompt or dataset name.
func pathUnescape(segment string) (string, error) {
	return url.PathUnescape(segment)
}

// promptMessage is one message of a stored prompt version, in the chat shape SDKs expect. Content
// is text, or the content parts when the message holds more than text (an image).
type promptMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// promptVersionMessages reads a version's messages. The prompt repository stores each message as
// the dashboard's serialized form: {originalType, payload: {role, content}}. Only request messages
// are part of a prompt. Text parts are joined to text; a message with any other part keeps its
// parts, and multimodal reports that one did.
func promptVersionMessages(version *tables.TablePromptVersion) (messages []promptMessage, multimodal bool) {
	messages = make([]promptMessage, 0, len(version.Messages))
	for _, stored := range version.Messages {
		var serialized struct {
			CurrentType string `json:"currentType"`
			Payload     struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"payload"`
		}
		if err := sonic.Unmarshal(stored.Message, &serialized); err != nil || serialized.Payload.Role == "" {
			continue
		}
		if serialized.CurrentType != "" && serialized.CurrentType != "completion_request" {
			continue
		}
		var text strings.Builder
		var content any
		switch stored := serialized.Payload.Content.(type) {
		case string:
			text.WriteString(stored)
		case []any:
			for _, part := range stored {
				object, _ := part.(map[string]any)
				value, isText := object["text"].(string)
				if !isText || (object["type"] != nil && object["type"] != "text") {
					content, multimodal = stored, true
					break
				}
				text.WriteString(value)
			}
		}
		if content == nil {
			content = text.String()
		}
		messages = append(messages, promptMessage{Role: serialized.Payload.Role, Content: content})
	}
	return messages, multimodal
}

// promptSlug is the slug of a prompt name: lowercase, with every run of other characters a dash.
func promptSlug(name string) string {
	var slug strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
			dash = false
		} else if !dash && slug.Len() > 0 {
			slug.WriteRune('-')
			dash = true
		}
	}
	return strings.TrimRight(slug.String(), "-")
}

// promptReader authenticates a prompt fetch. Reading prompts is a permission a tracing key has
// only when it was given: by default a key writes traces and does nothing else.
func (h *TraceIngestHandler) promptReader(ctx *fasthttp.RequestCtx) (*traceingest.Principal, bool) {
	principal, ok := h.authenticate(ctx, bearerCredential(string(ctx.Request.Header.Peek("Authorization"))))
	if !ok {
		return nil, false
	}
	if !principal.CanReadPrompts && !principal.AllProjects {
		SendError(ctx, fasthttp.StatusForbidden, "this tracing key may not read prompts. Turn on prompt access for the key under Observability > Tracing.")
		return nil, false
	}
	if h.prompts == nil {
		SendError(ctx, fasthttp.StatusNotFound, "the prompt repository is not available")
		return nil, false
	}
	return principal, true
}

// matchPrompt picks the prompt a reference names: an exact name first, then a slug alias. An alias
// that more than one prompt shares is errPromptAmbiguous.
func matchPrompt(prompts []tables.TablePrompt, ref string) (*tables.TablePrompt, error) {
	for i := range prompts {
		if prompts[i].Name == ref {
			return &prompts[i], nil
		}
	}
	slug := promptSlug(ref)
	var match *tables.TablePrompt
	for i := range prompts {
		if promptSlug(prompts[i].Name) != slug {
			continue
		}
		if match != nil {
			return nil, errPromptAmbiguous
		}
		match = &prompts[i]
	}
	return match, nil
}

// findPromptVersion finds the prompt a reference names and the version a fetch asks for (a number,
// or the newest), with its messages. version is nil when there is no such prompt or version.
func (h *TraceIngestHandler) findPromptVersion(ctx context.Context, ref string, number int) (*tables.TablePrompt, *tables.TablePromptVersion, error) {
	if index, ok := h.prompts.(tracePromptIndex); ok {
		prompts, err := index.ListPromptSummaries(ctx)
		if err != nil {
			return nil, nil, err
		}
		prompt, err := matchPrompt(prompts, ref)
		if err != nil || prompt == nil {
			return nil, nil, err
		}
		summaries, err := index.ListPromptVersionSummaries(ctx, []string{prompt.ID})
		if err != nil {
			return nil, nil, err
		}
		picked := pickPromptVersion(summaries, number)
		if picked == nil {
			return prompt, nil, nil
		}
		version, err := h.prompts.GetPromptVersionByID(ctx, picked.ID)
		return prompt, version, err
	}
	prompts, err := h.prompts.GetPrompts(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	prompt, err := matchPrompt(prompts, ref)
	if err != nil || prompt == nil {
		return nil, nil, err
	}
	versions, err := h.prompts.GetPromptVersions(ctx, prompt.ID)
	if err != nil {
		return nil, nil, err
	}
	return prompt, pickPromptVersion(versions, number), nil
}

// pickPromptVersion chooses the version a fetch asks for: a number, or the newest. versions are
// newest first.
func pickPromptVersion(versions []tables.TablePromptVersion, number int) *tables.TablePromptVersion {
	if len(versions) == 0 {
		return nil
	}
	if number <= 0 {
		return &versions[0]
	}
	for i := range versions {
		if versions[i].VersionNumber == number {
			return &versions[i]
		}
	}
	return nil
}

// promptConfig is the model settings of a version, as the config SDKs hand to the model call.
func promptConfig(version *tables.TablePromptVersion) map[string]any {
	config := make(map[string]any, len(version.ModelParams)+2)
	for key, value := range version.ModelParams {
		config[key] = value
	}
	if version.Model != "" {
		config["model"] = version.Model
	}
	if version.Provider != "" {
		config["provider"] = version.Provider
	}
	return config
}

func langfusePromptLabels(version *tables.TablePromptVersion) []string {
	if version.IsLatest {
		return []string{"production", "latest"}
	}
	return []string{}
}

// langfuseGetPrompt serves GET /api/public/v2/prompts/{name}. Bifrost's newest version carries the
// labels "production" and "latest", so a fetch with no version and no label gets it, as the SDK
// expects. Every prompt is returned as a chat prompt.
func (h *TraceIngestHandler) langfuseGetPrompt(ctx *fasthttp.RequestCtx) {
	if _, ok := h.promptReader(ctx); !ok {
		return
	}
	name, _ := ctx.UserValue("name").(string)
	// The SDK escapes the prompt name into the path.
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	number := 0
	if raw := string(ctx.QueryArgs().Peek("version")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			langfuseError(ctx, fasthttp.StatusBadRequest, "version must be a positive number")
			return
		}
		number = parsed
	}
	if label := string(ctx.QueryArgs().Peek("label")); label != "" && label != "production" && label != "latest" && number == 0 {
		langfuseError(ctx, fasthttp.StatusNotFound, "prompt has no version with label "+label)
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	prompt, version, err := h.findPromptVersion(storeCtx, name, number)
	if errors.Is(err, errPromptAmbiguous) {
		langfuseError(ctx, fasthttp.StatusConflict, err.Error())
		return
	}
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if version == nil {
		langfuseError(ctx, fasthttp.StatusNotFound, "prompt not found")
		return
	}
	stored, multimodal := promptVersionMessages(version)
	if multimodal {
		// A Langfuse chat message carries text content only; flattening would drop the image.
		langfuseError(ctx, fasthttp.StatusUnprocessableEntity, "this prompt has image content, which a Langfuse chat prompt cannot carry")
		return
	}
	messages := make([]map[string]any, 0, len(stored))
	for _, message := range stored {
		messages = append(messages, map[string]any{"type": "chatmessage", "role": message.Role, "content": message.Content})
	}
	SendJSON(ctx, map[string]any{
		"type": "chat", "name": prompt.Name, "version": version.VersionNumber, "prompt": messages, "config": promptConfig(version),
		"labels": langfusePromptLabels(version), "tags": []string{}, "commitMessage": version.CommitMessage, "resolutionGraph": nil,
	})
}

// langfuseListPrompts serves GET /api/public/v2/prompts.
func (h *TraceIngestHandler) langfuseListPrompts(ctx *fasthttp.RequestCtx) {
	if _, ok := h.promptReader(ctx); !ok {
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	prompts, versionsOf, err := h.listPromptVersions(storeCtx)
	if err != nil {
		h.fail(ctx, err)
		return
	}
	filter := string(ctx.QueryArgs().Peek("name"))
	data := make([]map[string]any, 0, len(prompts))
	for i := range prompts {
		prompt := &prompts[i]
		if filter != "" && prompt.Name != filter {
			continue
		}
		versions, err := versionsOf(prompt.ID)
		if err != nil {
			h.fail(ctx, err)
			return
		}
		if len(versions) == 0 {
			continue
		}
		numbers := make([]int, len(versions))
		for j := range versions {
			numbers[j] = versions[j].VersionNumber
		}
		data = append(data, map[string]any{
			"name": prompt.Name, "type": "chat", "versions": numbers, "labels": []string{"production", "latest"}, "tags": []string{},
			"lastUpdatedAt": langfuseTimestamp(versions[0].CreatedAt), "lastConfig": promptConfig(&versions[0]),
		})
	}
	SendJSON(ctx, map[string]any{"data": data, "meta": map[string]any{"page": 1, "limit": max(len(data), 1), "totalItems": len(data), "totalPages": 1}})
}

// listPromptVersions lists the prompts and returns a lookup of each one's versions, newest first.
// With the metadata index this is two queries and no message bodies.
func (h *TraceIngestHandler) listPromptVersions(ctx context.Context) ([]tables.TablePrompt, func(promptID string) ([]tables.TablePromptVersion, error), error) {
	index, ok := h.prompts.(tracePromptIndex)
	if !ok {
		prompts, err := h.prompts.GetPrompts(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		return prompts, func(promptID string) ([]tables.TablePromptVersion, error) {
			return h.prompts.GetPromptVersions(ctx, promptID)
		}, nil
	}
	prompts, err := index.ListPromptSummaries(ctx)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, len(prompts))
	for i := range prompts {
		ids[i] = prompts[i].ID
	}
	summaries, err := index.ListPromptVersionSummaries(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	byPrompt := make(map[string][]tables.TablePromptVersion, len(prompts))
	for _, version := range summaries {
		byPrompt[version.PromptID] = append(byPrompt[version.PromptID], version)
	}
	return prompts, func(promptID string) ([]tables.TablePromptVersion, error) { return byPrompt[promptID], nil }, nil
}

// promptWriteRefused answers an SDK that tries to create a prompt. Prompts are written in the
// dashboard: a tracing key is not a credential that edits what production code runs.
func (h *TraceIngestHandler) promptWriteRefused(ctx *fasthttp.RequestCtx) {
	if _, ok := h.authenticate(ctx, bearerCredential(string(ctx.Request.Header.Peek("Authorization")))); ok {
		SendError(ctx, fasthttp.StatusForbidden, "prompts are created in the Bifrost prompt repository, not through a tracing key")
	}
}

// braintrustGetPrompt serves GET /v1/prompt, which load_prompt() calls with a slug and an optional
// version. The answer is {"objects": [...]}, empty when there is no such prompt.
func (h *TraceIngestHandler) braintrustGetPrompt(ctx *fasthttp.RequestCtx) {
	if _, ok := h.promptReader(ctx); !ok {
		return
	}
	args := ctx.QueryArgs()
	slug := string(args.Peek("slug"))
	if slug == "" {
		slug = string(args.Peek("prompt_name"))
	}
	number := 0
	if raw := string(args.Peek("version")); raw != "" {
		number, _ = strconv.Atoi(raw)
		if number < 1 {
			SendJSON(ctx, map[string]any{"objects": []any{}})
			return
		}
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	prompt, version, err := h.findPromptVersion(storeCtx, slug, number)
	if errors.Is(err, errPromptAmbiguous) {
		SendError(ctx, fasthttp.StatusConflict, err.Error())
		return
	}
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if version == nil {
		SendJSON(ctx, map[string]any{"objects": []any{}})
		return
	}
	// Braintrust's chat messages take OpenAI content parts, so an image part is returned as it is.
	braintrustMessages, _ := promptVersionMessages(version)
	params := make(map[string]any, len(version.ModelParams))
	for key, value := range version.ModelParams {
		params[key] = value
	}
	projectID := string(args.Peek("project_id"))
	if projectID == "" {
		projectID = string(args.Peek("project_name"))
	}
	SendJSON(ctx, map[string]any{"objects": []any{map[string]any{
		"id": prompt.ID, "_xact_id": strconv.Itoa(version.VersionNumber), "project_id": projectID, "name": prompt.Name, "slug": promptSlug(prompt.Name),
		"description": version.CommitMessage, "created": version.CreatedAt.UTC().Format(time.RFC3339Nano), "tags": nil, "metadata": nil,
		"prompt_data": map[string]any{
			"prompt":  map[string]any{"type": "chat", "messages": braintrustMessages},
			"options": map[string]any{"model": version.Model, "params": params},
		},
	}}})
}
