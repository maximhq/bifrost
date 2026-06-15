package deepseek

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// compile-time check that DeepSeekProvider implements schemas.Provider
var _ schemas.Provider = (*DeepSeekProvider)(nil)

// TestDeepSeekProviderKey verifies the provider key constant.
func TestDeepSeekProviderKey(t *testing.T) {
	p := &DeepSeekProvider{}
	if got := p.GetProviderKey(); got != schemas.DeepSeek {
		t.Errorf("GetProviderKey() = %q, want %q", got, schemas.DeepSeek)
	}
}

// TestDeepSeekInStandardProviders verifies DeepSeek is in the StandardProviders list.
func TestDeepSeekInStandardProviders(t *testing.T) {
	found := false
	for _, p := range schemas.StandardProviders {
		if p == schemas.DeepSeek {
			found = true
			break
		}
	}
	if !found {
		t.Error("schemas.DeepSeek not found in schemas.StandardProviders")
	}
}

// unsupportedOp represents an operation that DeepSeekProvider should reject.
type unsupportedOp struct {
	name        string
	requestType schemas.RequestType
	invoke      func(p *DeepSeekProvider) *schemas.BifrostError
}

// TestDeepSeekUnsupportedOperations verifies that all unsupported operations return
// errors with the correct request type and provider key in the error details.
func TestDeepSeekUnsupportedOperations(t *testing.T) {
	p := &DeepSeekProvider{}

	cases := []unsupportedOp{
		{name: "TextCompletion", requestType: schemas.TextCompletionRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.TextCompletion(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "TextCompletionStream", requestType: schemas.TextCompletionStreamRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.TextCompletionStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Embedding", requestType: schemas.EmbeddingRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.Embedding(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CountTokens", requestType: schemas.CountTokensRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.CountTokens(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Speech", requestType: schemas.SpeechRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.Speech(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "SpeechStream", requestType: schemas.SpeechStreamRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.SpeechStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Transcription", requestType: schemas.TranscriptionRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.Transcription(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "TranscriptionStream", requestType: schemas.TranscriptionStreamRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.TranscriptionStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageGeneration", requestType: schemas.ImageGenerationRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ImageGeneration(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageGenerationStream", requestType: schemas.ImageGenerationStreamRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ImageGenerationStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageEdit", requestType: schemas.ImageEditRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ImageEdit(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageEditStream", requestType: schemas.ImageEditStreamRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ImageEditStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageVariation", requestType: schemas.ImageVariationRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ImageVariation(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoGeneration", requestType: schemas.VideoGenerationRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.VideoGeneration(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoRetrieve", requestType: schemas.VideoRetrieveRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.VideoRetrieve(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoDownload", requestType: schemas.VideoDownloadRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.VideoDownload(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoDelete", requestType: schemas.VideoDeleteRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.VideoDelete(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoList", requestType: schemas.VideoListRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.VideoList(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoRemix", requestType: schemas.VideoRemixRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.VideoRemix(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Rerank", requestType: schemas.RerankRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.Rerank(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "OCR", requestType: schemas.OCRRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.OCR(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "BatchCreate", requestType: schemas.BatchCreateRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.BatchCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "BatchList", requestType: schemas.BatchListRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.BatchList(nil, nil, nil)
			return err
		}},
		{name: "BatchRetrieve", requestType: schemas.BatchRetrieveRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.BatchRetrieve(nil, nil, nil)
			return err
		}},
		{name: "BatchCancel", requestType: schemas.BatchCancelRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.BatchCancel(nil, nil, nil)
			return err
		}},
		{name: "BatchDelete", requestType: schemas.BatchDeleteRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.BatchDelete(nil, nil, nil)
			return err
		}},
		{name: "BatchResults", requestType: schemas.BatchResultsRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.BatchResults(nil, nil, nil)
			return err
		}},
		{name: "FileUpload", requestType: schemas.FileUploadRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.FileUpload(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "FileList", requestType: schemas.FileListRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.FileList(nil, nil, nil)
			return err
		}},
		{name: "FileRetrieve", requestType: schemas.FileRetrieveRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.FileRetrieve(nil, nil, nil)
			return err
		}},
		{name: "FileDelete", requestType: schemas.FileDeleteRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.FileDelete(nil, nil, nil)
			return err
		}},
		{name: "FileContent", requestType: schemas.FileContentRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.FileContent(nil, nil, nil)
			return err
		}},
		{name: "Compaction", requestType: schemas.CompactionRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.Compaction(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ContainerCreate", requestType: schemas.ContainerCreateRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ContainerList", requestType: schemas.ContainerListRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerList(nil, nil, nil)
			return err
		}},
		{name: "ContainerRetrieve", requestType: schemas.ContainerRetrieveRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerRetrieve(nil, nil, nil)
			return err
		}},
		{name: "ContainerDelete", requestType: schemas.ContainerDeleteRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerDelete(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileCreate", requestType: schemas.ContainerFileCreateRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerFileCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ContainerFileList", requestType: schemas.ContainerFileListRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerFileList(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileRetrieve", requestType: schemas.ContainerFileRetrieveRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerFileRetrieve(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileContent", requestType: schemas.ContainerFileContentRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerFileContent(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileDelete", requestType: schemas.ContainerFileDeleteRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.ContainerFileDelete(nil, nil, nil)
			return err
		}},
		{name: "Passthrough", requestType: schemas.PassthroughRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.Passthrough(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "PassthroughStream", requestType: schemas.PassthroughStreamRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.PassthroughStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CachedContentCreate", requestType: schemas.CachedContentCreateRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.CachedContentCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CachedContentList", requestType: schemas.CachedContentListRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.CachedContentList(nil, nil, nil)
			return err
		}},
		{name: "CachedContentRetrieve", requestType: schemas.CachedContentRetrieveRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.CachedContentRetrieve(nil, nil, nil)
			return err
		}},
		{name: "CachedContentUpdate", requestType: schemas.CachedContentUpdateRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.CachedContentUpdate(nil, nil, nil)
			return err
		}},
		{name: "CachedContentDelete", requestType: schemas.CachedContentDeleteRequest, invoke: func(p *DeepSeekProvider) *schemas.BifrostError {
			_, err := p.CachedContentDelete(nil, nil, nil)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.invoke(p)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error == nil {
				t.Fatal("expected Error field, got nil")
			}
			wantMsg := string(tc.requestType) + " is not supported by " + string(schemas.DeepSeek) + " provider"
			if err.Error.Message != wantMsg {
				t.Errorf("Error.Message = %q, want %q", err.Error.Message, wantMsg)
			}
			if err.ExtraFields.Provider != schemas.DeepSeek {
				t.Errorf("ExtraFields.Provider = %q, want %q", err.ExtraFields.Provider, schemas.DeepSeek)
			}
			if err.ExtraFields.RequestType != tc.requestType {
				t.Errorf("ExtraFields.RequestType = %q, want %q", err.ExtraFields.RequestType, tc.requestType)
			}
		})
	}
}

// TestDeepSeekGetProviderKey verifies GetProviderKey returns the correct identifier.
func TestDeepSeekGetProviderKey(t *testing.T) {
	p := &DeepSeekProvider{}
	if got := p.GetProviderKey(); got != schemas.DeepSeek {
		t.Errorf("GetProviderKey() = %q, want %q", got, schemas.DeepSeek)
	}
}
