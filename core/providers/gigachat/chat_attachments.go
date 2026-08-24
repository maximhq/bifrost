package gigachat

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func (provider *GigaChatProvider) prepareGigaChatChatAttachments(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatRequest, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion request is nil", nil)
	}

	var prepared *schemas.BifrostChatRequest
	for messageIndex := range request.Input {
		content := request.Input[messageIndex].Content
		if content == nil || len(content.ContentBlocks) == 0 {
			continue
		}

		for blockIndex, block := range content.ContentBlocks {
			if gigaChatChatAttachmentMayUpload(block) {
				if replacement, ok := provider.getCachedGigaChatChatAttachment(ctx, key, request, messageIndex, blockIndex); ok {
					if prepared == nil {
						prepared = cloneGigaChatChatRequestForAttachmentUpload(request)
					}
					prepared.Input[messageIndex].Content.ContentBlocks[blockIndex] = replacement
					continue
				}
			}

			replacement, changed, bifrostErr := provider.prepareGigaChatChatAttachmentBlock(ctx, key, blockIndex, block)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			if !changed {
				continue
			}

			if prepared == nil {
				prepared = cloneGigaChatChatRequestForAttachmentUpload(request)
			}
			provider.setCachedGigaChatChatAttachment(ctx, key, request, messageIndex, blockIndex, replacement)
			prepared.Input[messageIndex].Content.ContentBlocks[blockIndex] = replacement
		}
	}

	if prepared != nil {
		return prepared, nil
	}
	return request, nil
}

func gigaChatChatAttachmentMayUpload(block schemas.ChatContentBlock) bool {
	switch block.Type {
	case schemas.ChatContentBlockTypeImage:
		return true
	case schemas.ChatContentBlockTypeFile:
		if block.File == nil {
			return false
		}
		return block.File.FileID == nil || strings.TrimSpace(*block.File.FileID) == ""
	default:
		return false
	}
}

func cloneGigaChatChatRequestForAttachmentUpload(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	prepared := *request
	prepared.Input = make([]schemas.ChatMessage, len(request.Input))
	copy(prepared.Input, request.Input)

	for i := range prepared.Input {
		if request.Input[i].Content == nil {
			continue
		}
		contentCopy := *request.Input[i].Content
		if request.Input[i].Content.ContentBlocks != nil {
			contentCopy.ContentBlocks = make([]schemas.ChatContentBlock, len(request.Input[i].Content.ContentBlocks))
			copy(contentCopy.ContentBlocks, request.Input[i].Content.ContentBlocks)
		}
		prepared.Input[i].Content = &contentCopy
	}

	return &prepared
}

func (provider *GigaChatProvider) prepareGigaChatChatAttachmentBlock(ctx *schemas.BifrostContext, key schemas.Key, blockIndex int, block schemas.ChatContentBlock) (schemas.ChatContentBlock, bool, *schemas.BifrostError) {
	switch block.Type {
	case schemas.ChatContentBlockTypeImage:
		upload, err := gigaChatChatImageUpload(blockIndex, block)
		if err != nil {
			return schemas.ChatContentBlock{}, false, providerUtils.NewBifrostOperationError(err.Error(), err)
		}
		return provider.uploadGigaChatChatAttachment(ctx, key, upload)
	case schemas.ChatContentBlockTypeFile:
		if block.File == nil {
			return block, false, nil
		}
		if block.File.FileID != nil && strings.TrimSpace(*block.File.FileID) != "" {
			return block, false, nil
		}
		if block.File.FileURL != nil && strings.TrimSpace(*block.File.FileURL) != "" {
			return schemas.ChatContentBlock{}, false, providerUtils.NewBifrostOperationError(
				fmt.Sprintf("content block %d: GigaChat chat file_url attachments are not supported; upload the file first and pass file_id", blockIndex),
				nil,
			)
		}
		if block.File.FileData == nil || strings.TrimSpace(*block.File.FileData) == "" {
			return block, false, nil
		}

		upload, err := gigaChatChatFileUpload(blockIndex, block.File)
		if err != nil {
			return schemas.ChatContentBlock{}, false, providerUtils.NewBifrostOperationError(err.Error(), err)
		}
		return provider.uploadGigaChatChatAttachment(ctx, key, upload)
	default:
		return block, false, nil
	}
}

type gigaChatChatAttachmentUpload struct {
	file        []byte
	filename    string
	contentType string
}

func (provider *GigaChatProvider) uploadGigaChatChatAttachment(ctx *schemas.BifrostContext, key schemas.Key, upload gigaChatChatAttachmentUpload) (schemas.ChatContentBlock, bool, *schemas.BifrostError) {
	uploadResp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
		Provider:    provider.GetProviderKey(),
		File:        upload.file,
		Filename:    upload.filename,
		Purpose:     schemas.FilePurposeUserData,
		ContentType: &upload.contentType,
	})
	if bifrostErr != nil {
		return schemas.ChatContentBlock{}, false, bifrostErr
	}
	if uploadResp == nil || strings.TrimSpace(uploadResp.ID) == "" {
		return schemas.ChatContentBlock{}, false, providerUtils.NewBifrostOperationError("GigaChat file upload response did not include file id", nil)
	}

	fileID := strings.TrimSpace(uploadResp.ID)
	return schemas.ChatContentBlock{
		Type: schemas.ChatContentBlockTypeFile,
		File: &schemas.ChatInputFile{
			FileID:   &fileID,
			Filename: &upload.filename,
			FileType: &upload.contentType,
		},
	}, true, nil
}

func gigaChatChatImageUpload(blockIndex int, block schemas.ChatContentBlock) (gigaChatChatAttachmentUpload, error) {
	if block.ImageURLStruct == nil || strings.TrimSpace(block.ImageURLStruct.URL) == "" {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: image_url.url is required", blockIndex)
	}

	sanitizedURL, err := schemas.SanitizeImageURL(block.ImageURLStruct.URL)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: invalid image_url: %w", blockIndex, err)
	}
	urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
	if urlInfo.Type != schemas.ImageContentTypeBase64 || urlInfo.DataURLWithoutPrefix == nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: GigaChat chat image_url supports base64 data URLs only; upload remote images first and pass file_id", blockIndex)
	}

	fileBytes, err := decodeGigaChatAttachmentBase64(*urlInfo.DataURLWithoutPrefix)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to decode image_url data: %w", blockIndex, err)
	}
	contentType := "image/jpeg"
	if urlInfo.MediaType != nil && strings.TrimSpace(*urlInfo.MediaType) != "" {
		contentType = normalizeGigaChatContentType(*urlInfo.MediaType)
	}

	return gigaChatChatAttachmentUpload{
		file:        fileBytes,
		filename:    "image" + extensionForGigaChatContentType(contentType),
		contentType: contentType,
	}, nil
}

func gigaChatChatFileUpload(blockIndex int, file *schemas.ChatInputFile) (gigaChatChatAttachmentUpload, error) {
	contentType := strings.TrimSpace(valueOrEmpty(file.FileType))
	filename := strings.TrimSpace(valueOrEmpty(file.Filename))
	fileData := strings.TrimSpace(valueOrEmpty(file.FileData))
	if contentType == "" && filename != "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	}

	if dataURLContentType, dataURLPayload, isBase64, ok, err := parseGigaChatDataURL(fileData); err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: invalid file_data data URL: %w", blockIndex, err)
	} else if ok {
		if contentType == "" {
			contentType = dataURLContentType
		}
		if isBase64 {
			decoded, err := decodeGigaChatAttachmentBase64(dataURLPayload)
			if err != nil {
				return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to decode file_data: %w", blockIndex, err)
			}
			return gigaChatChatAttachmentUpload{
				file:        decoded,
				filename:    filenameForGigaChatAttachment(filename, contentType, "file"),
				contentType: normalizeGigaChatContentType(contentType),
			}, nil
		}
		return gigaChatChatAttachmentUpload{
			file:        []byte(dataURLPayload),
			filename:    filenameForGigaChatAttachment(filename, contentType, "file"),
			contentType: normalizeGigaChatContentType(contentType),
		}, nil
	}

	if isGigaChatTextContentType(contentType) {
		return gigaChatChatAttachmentUpload{
			file:        []byte(fileData),
			filename:    filenameForGigaChatAttachment(filename, contentType, "file"),
			contentType: normalizeGigaChatContentType(contentType),
		}, nil
	}

	decoded, err := decodeGigaChatAttachmentBase64(fileData)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: file_data must be a base64 data URL or base64-encoded content: %w", blockIndex, err)
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return gigaChatChatAttachmentUpload{
		file:        decoded,
		filename:    filenameForGigaChatAttachment(filename, contentType, "file"),
		contentType: normalizeGigaChatContentType(contentType),
	}, nil
}

func parseGigaChatDataURL(value string) (string, string, bool, bool, error) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false, false, nil
	}

	metaAndPayload := strings.TrimPrefix(value, "data:")
	meta, payload, ok := strings.Cut(metaAndPayload, ",")
	if !ok {
		return "", "", false, true, fmt.Errorf("missing comma separator")
	}

	contentType := "text/plain"
	isBase64 := false
	for index, part := range strings.Split(meta, ";") {
		if index == 0 && strings.TrimSpace(part) != "" {
			contentType = strings.TrimSpace(part)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			isBase64 = true
		}
	}
	if isBase64 {
		return normalizeGigaChatContentType(contentType), payload, true, true, nil
	}

	decodedPayload, err := url.PathUnescape(payload)
	if err != nil {
		return "", "", false, true, err
	}
	return normalizeGigaChatContentType(contentType), decodedPayload, false, true, nil
}

func decodeGigaChatAttachmentBase64(value string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, value)

	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, decoder := range decoders {
		decoded, err := decoder.DecodeString(cleaned)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func filenameForGigaChatAttachment(filename string, contentType string, fallbackBase string) string {
	if strings.TrimSpace(filename) != "" {
		return strings.TrimSpace(filename)
	}
	return fallbackBase + extensionForGigaChatContentType(contentType)
}

func extensionForGigaChatContentType(contentType string) string {
	switch normalizeGigaChatContentType(contentType) {
	case "image/jpeg":
		return ".jpg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".mp4"
	}
	if ext, err := mime.ExtensionsByType(normalizeGigaChatContentType(contentType)); err == nil && len(ext) > 0 {
		return ext[0]
	}
	return ""
}

func normalizeGigaChatContentType(contentType string) string {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	switch contentType {
	case "":
		return "application/octet-stream"
	case "image/jpg":
		return "image/jpeg"
	default:
		return contentType
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isGigaChatTextContentType(contentType string) bool {
	contentType = normalizeGigaChatContentType(contentType)
	return strings.HasPrefix(contentType, "text/") ||
		contentType == "application/json" ||
		contentType == "application/xml" ||
		contentType == "application/yaml" ||
		contentType == "application/x-yaml"
}

func gigaChatChatFileRequiresAutoFunctionCall(file *schemas.ChatInputFile) bool {
	if file == nil {
		return false
	}
	contentType := strings.TrimSpace(valueOrEmpty(file.FileType))
	if contentType == "" {
		filename := strings.TrimSpace(valueOrEmpty(file.Filename))
		if filename != "" {
			contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
		}
	}
	contentType = normalizeGigaChatContentType(contentType)
	return contentType != "" &&
		contentType != "application/octet-stream" &&
		!strings.HasPrefix(contentType, "image/")
}
