package gigachat

import (
	"context"
	"fmt"
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

type gigaChatResourceFetchFunc func(context.Context, string) (string, string, error)

func (provider *GigaChatProvider) prepareGigaChatResponsesAttachments(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses request is nil", nil)
	}

	var prepared *schemas.BifrostResponsesRequest
	for messageIndex := range request.Input {
		content := request.Input[messageIndex].Content
		if content == nil || len(content.ContentBlocks) == 0 {
			continue
		}

		for blockIndex, block := range content.ContentBlocks {
			if gigaChatResponsesAttachmentMayUpload(block) {
				if replacement, ok := provider.getCachedGigaChatResponsesAttachment(ctx, key, request, messageIndex, blockIndex); ok {
					if prepared == nil {
						prepared = cloneGigaChatResponsesRequestForAttachmentUpload(request)
					}
					prepared.Input[messageIndex].Content.ContentBlocks[blockIndex] = replacement
					continue
				}
			}

			replacement, changed, bifrostErr := provider.prepareGigaChatResponsesAttachmentBlock(ctx, key, blockIndex, block)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			if !changed {
				continue
			}

			if prepared == nil {
				prepared = cloneGigaChatResponsesRequestForAttachmentUpload(request)
			}
			provider.setCachedGigaChatResponsesAttachment(ctx, key, request, messageIndex, blockIndex, replacement)
			prepared.Input[messageIndex].Content.ContentBlocks[blockIndex] = replacement
		}
	}

	if prepared != nil {
		return prepared, nil
	}
	return request, nil
}

func gigaChatResponsesAttachmentMayUpload(block schemas.ResponsesMessageContentBlock) bool {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeImage:
		return block.ResponsesInputMessageContentBlockImage != nil &&
			block.ResponsesInputMessageContentBlockImage.ImageURL != nil &&
			strings.TrimSpace(*block.ResponsesInputMessageContentBlockImage.ImageURL) != ""
	case schemas.ResponsesInputMessageContentBlockTypeFile:
		file := block.ResponsesInputMessageContentBlockFile
		return file != nil &&
			((file.FileData != nil && strings.TrimSpace(*file.FileData) != "") ||
				(file.FileURL != nil && strings.TrimSpace(*file.FileURL) != ""))
	default:
		return false
	}
}

func cloneGigaChatResponsesRequestForAttachmentUpload(request *schemas.BifrostResponsesRequest) *schemas.BifrostResponsesRequest {
	prepared := *request
	prepared.Input = make([]schemas.ResponsesMessage, len(request.Input))
	for i := range request.Input {
		prepared.Input[i] = schemas.DeepCopyResponsesMessage(request.Input[i])
	}
	return &prepared
}

func (provider *GigaChatProvider) prepareGigaChatResponsesAttachmentBlock(ctx *schemas.BifrostContext, key schemas.Key, blockIndex int, block schemas.ResponsesMessageContentBlock) (schemas.ResponsesMessageContentBlock, bool, *schemas.BifrostError) {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage == nil ||
			block.ResponsesInputMessageContentBlockImage.ImageURL == nil ||
			strings.TrimSpace(*block.ResponsesInputMessageContentBlockImage.ImageURL) == "" {
			return block, false, nil
		}

		upload, err := gigaChatResponsesImageURLUpload(ctx, blockIndex, block, providerUtils.FetchAndEncodeURL)
		if err != nil {
			return schemas.ResponsesMessageContentBlock{}, false, providerUtils.NewBifrostOperationError(err.Error(), err)
		}
		return provider.uploadGigaChatResponsesAttachment(ctx, key, upload)
	case schemas.ResponsesInputMessageContentBlockTypeFile:
		file := block.ResponsesInputMessageContentBlockFile
		if file == nil {
			return block, false, nil
		}
		switch {
		case file.FileData != nil && strings.TrimSpace(*file.FileData) != "":
			upload, err := gigaChatResponsesInlineFileUpload(blockIndex, file)
			if err != nil {
				return schemas.ResponsesMessageContentBlock{}, false, providerUtils.NewBifrostOperationError(err.Error(), err)
			}
			return provider.uploadGigaChatResponsesAttachment(ctx, key, upload)
		case file.FileURL != nil && strings.TrimSpace(*file.FileURL) != "":
			upload, err := gigaChatResponsesFileURLUpload(ctx, blockIndex, file, providerUtils.FetchAndEncodeURL)
			if err != nil {
				return schemas.ResponsesMessageContentBlock{}, false, providerUtils.NewBifrostOperationError(err.Error(), err)
			}
			return provider.uploadGigaChatResponsesAttachment(ctx, key, upload)
		default:
			return block, false, nil
		}
	default:
		return block, false, nil
	}
}

func (provider *GigaChatProvider) uploadGigaChatResponsesAttachment(ctx *schemas.BifrostContext, key schemas.Key, upload gigaChatChatAttachmentUpload) (schemas.ResponsesMessageContentBlock, bool, *schemas.BifrostError) {
	uploadResp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
		Provider:    provider.GetProviderKey(),
		File:        upload.file,
		Filename:    upload.filename,
		Purpose:     schemas.FilePurposeUserData,
		ContentType: &upload.contentType,
	})
	if bifrostErr != nil {
		return schemas.ResponsesMessageContentBlock{}, false, bifrostErr
	}
	if uploadResp == nil || strings.TrimSpace(uploadResp.ID) == "" {
		return schemas.ResponsesMessageContentBlock{}, false, providerUtils.NewBifrostOperationError("GigaChat file upload response did not include file id", nil)
	}

	fileID := strings.TrimSpace(uploadResp.ID)
	return gigaChatResponsesUploadedFileBlock(fileID, upload.filename, upload.contentType), true, nil
}

func gigaChatResponsesUploadedFileBlock(fileID string, filename string, contentType string) schemas.ResponsesMessageContentBlock {
	file := &schemas.ResponsesInputMessageContentBlockFile{}
	if trimmedFilename := strings.TrimSpace(filename); trimmedFilename != "" {
		file.Filename = &trimmedFilename
	}
	if trimmedContentType := strings.TrimSpace(contentType); trimmedContentType != "" {
		file.FileType = &trimmedContentType
	}
	return schemas.ResponsesMessageContentBlock{
		Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
		FileID:                                schemas.Ptr(strings.TrimSpace(fileID)),
		ResponsesInputMessageContentBlockFile: file,
	}
}

func gigaChatResponsesImageURLUpload(ctx context.Context, blockIndex int, block schemas.ResponsesMessageContentBlock, fetch gigaChatResourceFetchFunc) (gigaChatChatAttachmentUpload, error) {
	if block.ResponsesInputMessageContentBlockImage == nil || block.ResponsesInputMessageContentBlockImage.ImageURL == nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: image_url is required", blockIndex)
	}

	imageURL := strings.TrimSpace(*block.ResponsesInputMessageContentBlockImage.ImageURL)
	sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: invalid image_url: %w", blockIndex, err)
	}

	urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
	contentType := "image/jpeg"
	if urlInfo.MediaType != nil && strings.TrimSpace(*urlInfo.MediaType) != "" {
		contentType = normalizeGigaChatContentType(*urlInfo.MediaType)
	}

	if urlInfo.Type == schemas.ImageContentTypeBase64 {
		if urlInfo.DataURLWithoutPrefix == nil {
			return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: image_url base64 payload is required", blockIndex)
		}
		fileBytes, err := decodeGigaChatAttachmentBase64(*urlInfo.DataURLWithoutPrefix)
		if err != nil {
			return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to decode image_url data: %w", blockIndex, err)
		}
		return gigaChatChatAttachmentUpload{
			file:        fileBytes,
			filename:    "image" + extensionForGigaChatContentType(contentType),
			contentType: contentType,
		}, nil
	}

	if fetch == nil {
		fetch = providerUtils.FetchAndEncodeURL
	}
	fetchedContentType, fetchedBase64, err := fetch(ctx, sanitizedURL)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to fetch image_url: %w", blockIndex, err)
	}
	if fetchedContentType = normalizeGigaChatFetchedContentType(fetchedContentType); fetchedContentType != "" {
		contentType = fetchedContentType
	}

	fileBytes, err := decodeGigaChatAttachmentBase64(fetchedBase64)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to decode fetched image_url data: %w", blockIndex, err)
	}
	return gigaChatChatAttachmentUpload{
		file:        fileBytes,
		filename:    filenameForGigaChatRemoteAttachment(sanitizedURL, "image", contentType),
		contentType: contentType,
	}, nil
}

func gigaChatResponsesInlineFileUpload(blockIndex int, file *schemas.ResponsesInputMessageContentBlockFile) (gigaChatChatAttachmentUpload, error) {
	if file == nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: file block is missing file payload", blockIndex)
	}
	return gigaChatChatFileUpload(blockIndex, &schemas.ChatInputFile{
		Filename: file.Filename,
		FileData: file.FileData,
		FileType: file.FileType,
	})
}

func gigaChatResponsesFileURLUpload(ctx context.Context, blockIndex int, file *schemas.ResponsesInputMessageContentBlockFile, fetch gigaChatResourceFetchFunc) (gigaChatChatAttachmentUpload, error) {
	if file == nil || file.FileURL == nil || strings.TrimSpace(*file.FileURL) == "" {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: file_url is required", blockIndex)
	}

	fileURL := strings.TrimSpace(*file.FileURL)
	if fetch == nil {
		fetch = providerUtils.FetchAndEncodeURL
	}
	fetchedContentType, fetchedBase64, err := fetch(ctx, fileURL)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to fetch file_url: %w", blockIndex, err)
	}

	fileBytes, err := decodeGigaChatAttachmentBase64(fetchedBase64)
	if err != nil {
		return gigaChatChatAttachmentUpload{}, fmt.Errorf("content block %d: failed to decode fetched file_url data: %w", blockIndex, err)
	}

	filename := strings.TrimSpace(valueOrEmpty(file.Filename))
	if filename == "" {
		filename = filenameFromGigaChatRemoteURL(fileURL)
	}
	contentType := strings.TrimSpace(valueOrEmpty(file.FileType))
	if contentType == "" {
		contentType = normalizeGigaChatFetchedContentType(fetchedContentType)
	}
	if contentType == "" {
		contentType = inferGigaChatContentTypeFromName(filename)
	}
	contentType = normalizeGigaChatContentType(contentType)

	return gigaChatChatAttachmentUpload{
		file:        fileBytes,
		filename:    filenameForGigaChatAttachment(filename, contentType, "file"),
		contentType: contentType,
	}, nil
}

func normalizeGigaChatFetchedContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mediaType
	}
	return normalizeGigaChatContentType(contentType)
}

func filenameForGigaChatRemoteAttachment(resourceURL string, fallbackBase string, contentType string) string {
	if filename := filenameFromGigaChatRemoteURL(resourceURL); filename != "" {
		return filename
	}
	return filenameForGigaChatAttachment("", contentType, fallbackBase)
}

func filenameFromGigaChatRemoteURL(resourceURL string) string {
	parsedURL, err := url.Parse(resourceURL)
	if err != nil {
		return ""
	}
	filename := path.Base(parsedURL.Path)
	if filename == "." || filename == "/" {
		return ""
	}
	return strings.TrimSpace(filename)
}

func inferGigaChatContentTypeFromName(filename string) string {
	if filename == "" {
		return ""
	}
	if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); contentType != "" {
		return normalizeGigaChatFetchedContentType(contentType)
	}
	return normalizeGigaChatFetchedContentType(mime.TypeByExtension(strings.ToLower(path.Ext(filename))))
}
