// Package maxim provides attachment extraction from Bifrost requests for Maxim logging.
package maxim

import (
	"encoding/base64"
	"log"
	"net/url"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/maxim-go/logging"
)

// ExtractAttachmentsFromRequest extracts image_url, file, and input_audio blocks from
// Chat and Responses API messages and converts them to maxim-go attachment types.
// Returns a slice of *logging.UrlAttachment or *logging.FileDataAttachment for use with
// Logger.GenerationAddAttachment.
func ExtractAttachmentsFromRequest(req *schemas.BifrostRequest) []interface{} {
	if req == nil {
		return nil
	}

	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		return extractFromChatRequest(req.ChatRequest)
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		return extractFromResponsesRequest(req.ResponsesRequest)
	default:
		return nil
	}
}

func extractFromChatRequest(cr *schemas.BifrostChatRequest) []interface{} {
	if cr == nil || cr.Input == nil {
		return nil
	}

	var attachments []interface{}
	for _, msg := range cr.Input {
		if msg.Content == nil || msg.Content.ContentBlocks == nil {
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			if att := chatBlockToAttachment(block); att != nil {
				attachments = append(attachments, att)
			}
		}
	}
	return attachments
}

func extractFromResponsesRequest(rr *schemas.BifrostResponsesRequest) []interface{} {
	if rr == nil || rr.Input == nil {
		return nil
	}

	var attachments []interface{}
	for _, msg := range rr.Input {
		if msg.Content == nil || msg.Content.ContentBlocks == nil {
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			if att := responsesBlockToAttachment(block); att != nil {
				attachments = append(attachments, att)
			}
		}
	}
	return attachments
}

func chatBlockToAttachment(block schemas.ChatContentBlock) interface{} {
	switch block.Type {
	case schemas.ChatContentBlockTypeImage:
		if block.ImageURLStruct != nil && block.ImageURLStruct.URL != "" {
			return urlToAttachment(block.ImageURLStruct.URL, "image")
		}
	case schemas.ChatContentBlockTypeFile:
		if block.File != nil {
			return chatFileToAttachment(block.File)
		}
	case schemas.ChatContentBlockTypeInputAudio:
		if block.InputAudio != nil && block.InputAudio.Data != "" {
			return audioDataToAttachment(block.InputAudio.Data, block.InputAudio.Format)
		}
	}
	return nil
}

func responsesBlockToAttachment(block schemas.ResponsesMessageContentBlock) interface{} {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ImageURL != nil && *block.ImageURL != "" {
			return urlToAttachment(*block.ImageURL, "image")
		}
	case schemas.ResponsesInputMessageContentBlockTypeFile:
		return responsesFileToAttachment(&block)
	case schemas.ResponsesInputMessageContentBlockTypeAudio:
		if block.Audio != nil && block.Audio.Data != "" {
			format := block.Audio.Format
			if format == "" {
				format = "mp3"
			}
			return audioDataToAttachment(block.Audio.Data, &format)
		}
	}
	return nil
}

func responsesFileToAttachment(block *schemas.ResponsesMessageContentBlock) interface{} {
	if block.FileURL != nil && *block.FileURL != "" {
		name := "attachment"
		if block.Filename != nil && *block.Filename != "" {
			name = *block.Filename
		}
		mime := ""
		if block.FileType != nil {
			mime = *block.FileType
		}
		urlStr := *block.FileURL
		return &logging.UrlAttachment{
			BaseAttachmentProps: logging.BaseAttachmentProps{
				ID:       uuid.New().String(),
				Name:     name,
				MimeType: mime,
				Metadata: map[string]string{"url": urlStr},
			},
			Type: logging.AttachmentTypeURL,
			URL:  urlStr,
		}
	}
	if block.FileData != nil && *block.FileData != "" {
		data, err := base64.StdEncoding.DecodeString(*block.FileData)
		if err != nil {
			log.Printf("%s failed to decode file_data base64: %v", PluginLoggerPrefix, err)
			return nil
		}
		name := "attachment"
		if block.Filename != nil && *block.Filename != "" {
			name = *block.Filename
		}
		mime := "application/octet-stream"
		if block.FileType != nil && *block.FileType != "" {
			mime = *block.FileType
		}
		return &logging.FileDataAttachment{
			BaseAttachmentProps: logging.BaseAttachmentProps{
				ID:       uuid.New().String(),
				Name:     name,
				MimeType: mime,
			},
			Type: logging.AttachmentTypeFileData,
			Data: data,
		}
	}
	return nil
}

func chatFileToAttachment(f *schemas.ChatInputFile) interface{} {
	if f.FileURL != nil && *f.FileURL != "" {
		name := "attachment"
		if f.Filename != nil && *f.Filename != "" {
			name = *f.Filename
		}
		mime := ""
		if f.FileType != nil {
			mime = *f.FileType
		}
		urlStr := *f.FileURL
		return &logging.UrlAttachment{
			BaseAttachmentProps: logging.BaseAttachmentProps{
				ID:       uuid.New().String(),
				Name:     name,
				MimeType: mime,
				Metadata: map[string]string{"url": urlStr},
			},
			Type: logging.AttachmentTypeURL,
			URL:  urlStr,
		}
	}
	if f.FileData != nil && *f.FileData != "" {
		data, err := base64.StdEncoding.DecodeString(*f.FileData)
		if err != nil {
			log.Printf("%s failed to decode file_data base64: %v", PluginLoggerPrefix, err)
			return nil
		}
		name := "attachment"
		if f.Filename != nil && *f.Filename != "" {
			name = *f.Filename
		}
		mime := "application/octet-stream"
		if f.FileType != nil && *f.FileType != "" {
			mime = *f.FileType
		}
		return &logging.FileDataAttachment{
			BaseAttachmentProps: logging.BaseAttachmentProps{
				ID:       uuid.New().String(),
				Name:     name,
				MimeType: mime,
			},
			Type: logging.AttachmentTypeFileData,
			Data: data,
		}
	}
	return nil
}

func audioDataToAttachment(data string, format *string) interface{} {
	// Data can be base64 or a data URL (data:audio/wav;base64,...)
	var b64 string
	if strings.HasPrefix(data, "data:") {
		idx := strings.Index(data, ";base64,")
		if idx == -1 {
			log.Printf("%s invalid audio data URL format", PluginLoggerPrefix)
			return nil
		}
		b64 = data[idx+8:]
	} else {
		b64 = data
	}

	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("%s failed to decode audio base64: %v", PluginLoggerPrefix, err)
		return nil
	}

	mime := "audio/mpeg"
	if format != nil {
		switch strings.ToLower(*format) {
		case "wav":
			mime = "audio/wav"
		case "mp3":
			mime = "audio/mpeg"
		default:
			mime = "audio/" + *format
		}
	}

	return &logging.FileDataAttachment{
		BaseAttachmentProps: logging.BaseAttachmentProps{
			ID:       uuid.New().String(),
			Name:     "audio." + extFromMime(mime),
			MimeType: mime,
		},
		Type: logging.AttachmentTypeFileData,
		Data: decoded,
	}
}

func urlToAttachment(urlStr string, kind string) interface{} {
	if strings.HasPrefix(urlStr, "data:") {
		return dataURLToAttachment(urlStr, kind)
	}
	// HTTP/HTTPS URL
	name := "attachment"
	if u, err := url.Parse(urlStr); err == nil {
		if p := path.Base(u.Path); p != "" && p != "." {
			name = p
		}
	}
	return &logging.UrlAttachment{
		BaseAttachmentProps: logging.BaseAttachmentProps{
			ID:       uuid.New().String(),
			Name:     name,
			Metadata: map[string]string{"url": urlStr},
		},
		Type: logging.AttachmentTypeURL,
		URL:  urlStr,
	}
}

func dataURLToAttachment(dataURL string, kind string) interface{} {
	// Format: data:image/png;base64,iVBORw0...
	idx := strings.Index(dataURL, ";base64,")
	if idx == -1 {
		log.Printf("%s invalid data URL format", PluginLoggerPrefix)
		return nil
	}

	header := dataURL[5:idx] // "image/png" or "image/png;charset=..."
	mime := strings.Split(header, ";")[0]
	if mime == "" {
		mime = "application/octet-stream"
	}

	b64 := dataURL[idx+8:]
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("%s failed to decode data URL base64: %v", PluginLoggerPrefix, err)
		return nil
	}

	name := kind + "." + extFromMime(mime)
	return &logging.FileDataAttachment{
		BaseAttachmentProps: logging.BaseAttachmentProps{
			ID:       uuid.New().String(),
			Name:     name,
			MimeType: mime,
		},
		Type: logging.AttachmentTypeFileData,
		Data: decoded,
	}
}

func extFromMime(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "audio/wav":
		return "wav"
	case "audio/mpeg":
		return "mp3"
	case "application/pdf":
		return "pdf"
	default:
		return "bin"
	}
}
