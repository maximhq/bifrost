package anthropic

import (
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// InjectRawMessageCacheBreakpoints applies configured checkpoints without rebuilding
// the native body. Conversion is used only as a source-mapped projection for index
// selection: native content blocks can expand into several Responses items. Unknown
// fields, signatures and key ordering remain in the original bytes.
func InjectRawMessageCacheBreakpoints(ctx *schemas.BifrostContext, cfg *schemas.PromptCacheConfig, body []byte, provider schemas.ModelProvider, model string) ([]byte, error) {
	if cfg == nil || (!cfg.AutoInject && len(cfg.InjectionPoints) == 0) {
		return body, nil
	}
	var native AnthropicMessageRequest
	if err := schemas.Unmarshal(body, &native); err != nil {
		return nil, fmt.Errorf("decode native request for prompt cache injection: %w", err)
	}
	// Caller-owned checkpoints, including top-level automatic caching and tools,
	// must win before we construct the projection.
	if native.CacheControl != nil {
		return body, nil
	}
	for _, tool := range native.Tools {
		if tool.CacheControl != nil {
			return body, nil
		}
	}
	hasMarker := func(content *AnthropicContent) bool {
		if content != nil {
			for _, block := range content.ContentBlocks {
				if block.CacheControl != nil {
					return true
				}
			}
		}
		return false
	}
	if hasMarker(native.System) {
		return body, nil
	}
	for i := range native.Messages {
		if hasMarker(&native.Messages[i].Content) {
			return body, nil
		}
	}

	type source struct {
		path    string
		promote bool
	}
	// The converter carries CacheControl pointers through to their normalized
	// counterparts. Private pointer identities give us exact source locations even
	// for repeated text and grouped tool results; none of these tags are serialized.
	sources := make(map[*schemas.CacheControl]source)
	tag := func(content *AnthropicContent, path string) {
		if content == nil {
			return
		}
		if content.ContentStr != nil {
			if *content.ContentStr == "" {
				return
			}
			marker := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
			sources[marker] = source{path: path, promote: true}
			content.ContentBlocks = []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: content.ContentStr, CacheControl: marker}}
			content.ContentStr = nil
			return
		}
		for i := range content.ContentBlocks {
			block := &content.ContentBlocks[i]
			switch block.Type {
			case AnthropicContentBlockTypeText, AnthropicContentBlockTypeImage, AnthropicContentBlockTypeDocument, AnthropicContentBlockTypeToolResult:
				marker := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
				sources[marker] = source{path: fmt.Sprintf("%s.%d.cache_control", path, i)}
				block.CacheControl = marker
			}
		}
	}
	tag(native.System, "system")
	for i := range native.Messages {
		tag(&native.Messages[i].Content, fmt.Sprintf("messages.%d.content", i))
	}
	// Match the ingress converter's grouping decision, not the outbound provider:
	// an alias or fallback can select a different provider after normalization.
	ingressProvider, _ := schemas.ParseModelString(native.Model, "")
	input := ConvertAnthropicMessagesToBifrostMessages(ctx, native.Messages, native.System, false, ingressProvider == schemas.Bedrock)
	type position struct{ msg, block int }
	locations := make(map[position]source)
	for i := range input {
		if src, ok := sources[input[i].CacheControl]; ok {
			locations[position{i, -1}] = src
			input[i].CacheControl = nil
		}
		if input[i].Content != nil {
			for j := range input[i].Content.ContentBlocks {
				block := &input[i].Content.ContentBlocks[j]
				if src, ok := sources[block.CacheControl]; ok {
					locations[position{i, j}] = src
					block.CacheControl = nil
				}
			}
		}
	}
	marked := providerUtils.InjectResponsesCacheBreakpointsForProvider(cfg, input, provider, model)
	out := body
	written := make(map[string]bool)
	apply := func(pos position, marker *schemas.CacheControl) error {
		if marker == nil {
			return nil
		}
		src, ok := locations[pos]
		if !ok {
			return fmt.Errorf("prompt cache target has no native source at item %d block %d", pos.msg, pos.block)
		}
		if written[src.path] {
			return nil
		}
		encoded, err := providerUtils.MarshalSorted(marker)
		if err != nil {
			return err
		}
		if src.promote {
			// Preserve the original JSON string's escapes as well as its value.
			text := providerUtils.GetJSONField(body, src.path).Raw
			encoded = []byte(`[{"type":"text","text":` + text + `,"cache_control":` + string(encoded) + `}]`)
		}
		out, err = providerUtils.SetRawJSONField(out, src.path, encoded)
		written[src.path] = err == nil
		return err
	}
	for i := range marked {
		if err := apply(position{i, -1}, marked[i].CacheControl); err != nil {
			return nil, err
		}
		if marked[i].Content != nil {
			for j, block := range marked[i].Content.ContentBlocks {
				if err := apply(position{i, j}, block.CacheControl); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}
