package mcp

import (
	"cmp"
	"fmt"
	"path"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// toolResultContent turns an MCP tool result into tool message content: a text-only result stays one
// string, as before; media becomes content blocks. structuredContent is used only when there is no text.
func toolResultContent(result *mcp.CallToolResult, toolName string) *schemas.ChatMessageContent {
	if result == nil {
		return &schemas.ChatMessageContent{ContentStr: schemas.Ptr(fmt.Sprintf("MCP tool '%s' executed successfully", toolName))}
	}
	if text, ok := textOnlyResult(result); ok {
		if text == "" {
			text = fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
		}
		return &schemas.ChatMessageContent{ContentStr: &text}
	}
	blocks := make([]schemas.ChatContentBlock, 0, len(result.Content))
	hasText, hasMedia := false, false
	for _, item := range result.Content {
		block, ok := toolResultBlock(item)
		if !ok {
			continue
		}
		if block.Type == schemas.ChatContentBlockTypeText {
			hasText = true
		} else {
			hasMedia = true
		}
		blocks = append(blocks, block)
	}
	if !hasText && result.StructuredContent != nil {
		if encoded, err := schemas.MarshalSorted(result.StructuredContent); err == nil {
			blocks = append(blocks, textBlock(string(encoded)))
		}
	}
	if !hasMedia {
		var text strings.Builder
		for _, block := range blocks {
			text.WriteString(*block.Text)
		}
		if joined := strings.TrimSpace(text.String()); joined != "" {
			return &schemas.ChatMessageContent{ContentStr: &joined}
		}
		return &schemas.ChatMessageContent{ContentStr: schemas.Ptr(fmt.Sprintf("MCP tool '%s' executed successfully", toolName))}
	}
	return &schemas.ChatMessageContent{ContentBlocks: blocks}
}

// textOnlyResult is the result's text, trimmed, when it holds nothing else.
func textOnlyResult(result *mcp.CallToolResult) (string, bool) {
	if len(result.Content) == 0 && result.StructuredContent != nil {
		return "", false
	}
	var text string
	var joined *strings.Builder
	for i, item := range result.Content {
		var part string
		switch content := ContentValue(item).(type) {
		case mcp.TextContent:
			part = content.Text
		case nil:
		default:
			return "", false
		}
		if i == 0 {
			text = part
			continue
		}
		if joined == nil {
			joined = &strings.Builder{}
			joined.WriteString(text)
		}
		joined.WriteString(part)
	}
	if joined != nil {
		text = joined.String()
	}
	return strings.TrimSpace(text), true
}

// ContentValue returns item as a value, since MCP content arrives as either; nil for a nil pointer.
func ContentValue(item mcp.Content) mcp.Content {
	switch content := item.(type) {
	case *mcp.TextContent:
		if content != nil {
			return *content
		}
	case *mcp.ImageContent:
		if content != nil {
			return *content
		}
	case *mcp.AudioContent:
		if content != nil {
			return *content
		}
	case *mcp.EmbeddedResource:
		if content != nil {
			return *content
		}
	case *mcp.ResourceLink:
		if content != nil {
			return *content
		}
	default:
		return item
	}
	return nil
}

// ResourceValue returns embedded resource contents as a value; nil for a nil pointer.
func ResourceValue(resource mcp.ResourceContents) mcp.ResourceContents {
	switch contents := resource.(type) {
	case *mcp.TextResourceContents:
		if contents != nil {
			return *contents
		}
	case *mcp.BlobResourceContents:
		if contents != nil {
			return *contents
		}
	default:
		return resource
	}
	return nil
}

// toolResultBlock converts one MCP content item; ok is false for an empty pointer.
func toolResultBlock(item mcp.Content) (schemas.ChatContentBlock, bool) {
	switch content := ContentValue(item).(type) {
	case nil:
		return schemas.ChatContentBlock{}, false
	case mcp.TextContent:
		return textBlock(content.Text), true
	case mcp.ImageContent:
		return imageBlock(content.MIMEType, content.Data), true
	case mcp.AudioContent:
		return schemas.ChatContentBlock{
			Type:       schemas.ChatContentBlockTypeInputAudio,
			InputAudio: &schemas.ChatInputAudio{Data: content.Data, Format: audioFormat(content.MIMEType)},
		}, true
	case mcp.EmbeddedResource:
		switch contents := ResourceValue(content.Resource).(type) {
		case mcp.TextResourceContents:
			return textBlock(contents.Text), true
		case mcp.BlobResourceContents:
			return blobBlock(contents), true
		}
		return textBlock("[Embedded resource]"), true
	case mcp.ResourceLink:
		return textBlock(resourceLinkText(content.Name, content.URI, content.MIMEType)), true
	}
	if encoded, err := schemas.MarshalSorted(item); err == nil {
		return textBlock(string(encoded)), true
	}
	return schemas.ChatContentBlock{}, false
}

func blobBlock(contents mcp.BlobResourceContents) schemas.ChatContentBlock {
	if strings.HasPrefix(contents.MIMEType, "image/") {
		return imageBlock(contents.MIMEType, contents.Blob)
	}
	// A blob is base64 (MCP spec); as a data URL a text/plain one is not read as raw text downstream.
	mimeType := cmp.Or(strings.Join(strings.Fields(contents.MIMEType), ""), "application/octet-stream")
	file := &schemas.ChatInputFile{FileData: schemas.Ptr("data:" + mimeType + ";base64," + contents.Blob)}
	if name := path.Base(contents.URI); name != "" && name != "." && name != "/" {
		file.Filename = &name
	}
	if contents.MIMEType != "" {
		file.FileType = schemas.Ptr(contents.MIMEType)
	}
	return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: file}
}

func textBlock(text string) schemas.ChatContentBlock {
	return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &text}
}

func imageBlock(mimeType, data string) schemas.ChatContentBlock {
	return schemas.ChatContentBlock{
		Type:           schemas.ChatContentBlockTypeImage,
		ImageURLStruct: &schemas.ChatInputImage{URL: "data:" + mimeType + ";base64," + data},
	}
}

// audioFormat is the input_audio format for an audio MIME type ("audio/mpeg" is "mp3").
func audioFormat(mimeType string) *string {
	format := strings.TrimPrefix(strings.ToLower(mimeType), "audio/")
	switch format {
	case "", mimeType:
		return nil
	case "mpeg":
		format = "mp3"
	case "x-wav", "wave":
		format = "wav"
	}
	return &format
}

// resourceLinkText names a linked resource for a model, which cannot follow the link itself.
func resourceLinkText(name, uri, mimeType string) string {
	text := "[Resource: " + name + " " + uri
	if mimeType != "" {
		text += " (" + mimeType + ")"
	}
	return text + "]"
}
