package gemini

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// resolveGeminiFileID resolves a Gemini Files API resource name to the URI
// required by a fileData part. File converters stay pure; provider I/O lives
// in this preparation step.
func (provider *GeminiProvider) resolveGeminiFileID(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	fileID string,
) (string, string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", "", nil
	}

	response, bifrostErr := provider.fileRetrieveByKey(ctx, key, &schemas.BifrostFileRetrieveRequest{
		Provider: schemas.Gemini,
		FileID:   fileID,
	})
	if bifrostErr != nil {
		return "", "", fmt.Errorf("failed to resolve Gemini file %q: %s", fileID, bifrostErr.GetErrorString())
	}
	if response == nil || strings.TrimSpace(response.StorageURI) == "" {
		return "", "", fmt.Errorf("Gemini file %q did not return a storage URI", fileID)
	}

	return response.StorageURI, response.ContentType, nil
}

func (provider *GeminiProvider) resolveChatFileBlock(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	block *schemas.ChatContentBlock,
) error {
	if block == nil || block.File == nil || block.File.FileID == nil {
		return nil
	}
	if strings.TrimSpace(*block.File.FileID) == "" {
		return nil
	}
	if block.File.FileData != nil {
		return nil
	}
	if block.File.FileURL != nil && strings.TrimSpace(*block.File.FileURL) != "" {
		return nil
	}

	uri, contentType, err := provider.resolveGeminiFileID(ctx, key, *block.File.FileID)
	if err != nil {
		return err
	}
	block.File.FileURL = schemas.Ptr(uri)
	if block.File.FileType == nil && contentType != "" {
		block.File.FileType = schemas.Ptr(contentType)
	}
	return nil
}

func (provider *GeminiProvider) resolveResponsesFileBlock(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	block *schemas.ResponsesMessageContentBlock,
) error {
	if block == nil || block.FileID == nil || strings.TrimSpace(*block.FileID) == "" {
		return nil
	}
	if block.Type != schemas.ResponsesInputMessageContentBlockTypeFile &&
		block.Type != schemas.ResponsesInputMessageContentBlockTypeContainer {
		return nil
	}
	if block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileData != nil {
		return nil
	}
	if block.ResponsesInputMessageContentBlockFile != nil &&
		block.ResponsesInputMessageContentBlockFile.FileURL != nil &&
		strings.TrimSpace(*block.ResponsesInputMessageContentBlockFile.FileURL) != "" {
		return nil
	}

	uri, contentType, err := provider.resolveGeminiFileID(ctx, key, *block.FileID)
	if err != nil {
		return err
	}
	if block.ResponsesInputMessageContentBlockFile == nil {
		block.ResponsesInputMessageContentBlockFile = &schemas.ResponsesInputMessageContentBlockFile{}
	}
	block.ResponsesInputMessageContentBlockFile.FileURL = schemas.Ptr(uri)
	if block.ResponsesInputMessageContentBlockFile.FileType == nil && contentType != "" {
		block.ResponsesInputMessageContentBlockFile.FileType = schemas.Ptr(contentType)
	}
	return nil
}

// prepareChatRequestWithResolvedFiles makes an isolated request copy so
// retries and provider fallbacks still see the caller's original file ID.
func (provider *GeminiProvider) prepareChatRequestWithResolvedFiles(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatRequest, error) {
	if request == nil {
		return nil, nil
	}

	prepared := *request
	prepared.Input = make([]schemas.ChatMessage, len(request.Input))
	for i, message := range request.Input {
		prepared.Input[i] = schemas.DeepCopyChatMessage(message)
		if message.Content != nil {
			for j := range message.Content.ContentBlocks {
				originalFile := message.Content.ContentBlocks[j].File
				preparedBlock := &prepared.Input[i].Content.ContentBlocks[j]
				if originalFile != nil && preparedBlock.File != nil {
					if originalFile.FileURL != nil {
						fileURL := *originalFile.FileURL
						preparedBlock.File.FileURL = &fileURL
					}
					if originalFile.FileType != nil {
						fileType := *originalFile.FileType
						preparedBlock.File.FileType = &fileType
					}
				}
				if err := provider.resolveChatFileBlock(ctx, key, preparedBlock); err != nil {
					return nil, err
				}
			}
		}
	}

	return &prepared, nil
}

// prepareResponsesRequestWithResolvedFiles makes an isolated request copy and
// resolves file IDs in both normal content and multimodal function outputs.
func (provider *GeminiProvider) prepareResponsesRequestWithResolvedFiles(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesRequest, error) {
	if request == nil {
		return nil, nil
	}

	prepared := *request
	prepared.Input = make([]schemas.ResponsesMessage, len(request.Input))
	for i, message := range request.Input {
		prepared.Input[i] = schemas.DeepCopyResponsesMessage(message)
		if prepared.Input[i].Content != nil {
			for j := range prepared.Input[i].Content.ContentBlocks {
				if err := provider.resolveResponsesFileBlock(
					ctx,
					key,
					&prepared.Input[i].Content.ContentBlocks[j],
				); err != nil {
					return nil, err
				}
			}
		}
		if prepared.Input[i].ResponsesToolMessage != nil &&
			prepared.Input[i].ResponsesToolMessage.Output != nil {
			for j := range prepared.Input[i].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
				if err := provider.resolveResponsesFileBlock(
					ctx,
					key,
					&prepared.Input[i].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks[j],
				); err != nil {
					return nil, err
				}
			}
		}
	}
	return &prepared, nil
}
