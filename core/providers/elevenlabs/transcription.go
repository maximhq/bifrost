package elevenlabs

import (
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func ToElevenlabsTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *ElevenlabsTranscriptionRequest {
	if bifrostReq == nil {
		return nil
	}

	req := &ElevenlabsTranscriptionRequest{
		ModelID: bifrostReq.Model,
	}

	if bifrostReq.Input != nil && len(bifrostReq.Input.File) > 0 {
		req.File = bifrostReq.Input.File
	}

	if bifrostReq.Params == nil {
		return req
	}

	params := bifrostReq.Params

	if params.Language != nil {
		req.LanguageCode = params.Language
	}

	if params.TagAudioEvents != nil {
		req.TagAudioEvents = params.TagAudioEvents
	}

	if params.NumSpeakers != nil && *params.NumSpeakers > 0 {
		req.NumSpeakers = params.NumSpeakers
	}

	if params.TimestampsGranularity != nil && *params.TimestampsGranularity != "" {
		granularity := ElevenlabsTimestampsGranularity(*params.TimestampsGranularity)
		req.TimestampsGranularity = &granularity
	}

	if params.Diarize != nil {
		req.Diarize = params.Diarize
	}

	if params.DiarizationThreshold != nil && *params.DiarizationThreshold > 0 {
		req.DiarizationThreshold = params.DiarizationThreshold
	}

	if len(params.AdditionalFormats) > 0 {
		additionalFormats := make([]ElevenlabsAdditionalFormat, 0, len(params.AdditionalFormats))
		for _, format := range params.AdditionalFormats {
			if converted, ok := convertAdditionalFormat(format); ok {
				additionalFormats = append(additionalFormats, converted)
			}
		}
		if len(additionalFormats) > 0 {
			req.AdditionalFormats = additionalFormats
		}
	}

	if params.FileFormat != nil && *params.FileFormat != "" {
		fileFormat := ElevenlabsFileFormat(*params.FileFormat)
		req.FileFormat = &fileFormat
	}

	if params.CloudStorageURL != nil {
		trimmed := strings.TrimSpace(*params.CloudStorageURL)
		if trimmed != "" {
			req.CloudStorageURL = schemas.Ptr(trimmed)
		}
	}

	if params.Webhook != nil {
		req.Webhook = params.Webhook
	}

	if params.WebhookID != nil {
		trimmed := strings.TrimSpace(*params.WebhookID)
		if trimmed != "" {
			req.WebhookID = schemas.Ptr(trimmed)
		}
	}

	if params.Temparature != nil {
		req.Temparature = params.Temparature
	}

	if params.Seed != nil {
		req.Seed = params.Seed
	}

	if params.UseMultiChannel != nil {
		req.UseMultiChannel = params.UseMultiChannel
	}

	if params.WebhookMetadata != nil {
		if metadataMap, ok := params.WebhookMetadata.(map[string]interface{}); ok {
			if len(metadataMap) > 0 {
				req.WebhookMetadata = metadataMap
			}
		} else {
			req.WebhookMetadata = params.WebhookMetadata
		}
	}

	return req
}

func convertAdditionalFormat(format schemas.BifrostTranscriptionAdditionalFormat) (ElevenlabsAdditionalFormat, bool) {
	if format.Format == "" {
		return ElevenlabsAdditionalFormat{}, false
	}

	converted := ElevenlabsAdditionalFormat{
		Format: ElevenlabsExportOptions(format.Format),
	}

	if format.IncludeSpeakers != nil {
		converted.IncludeSpeakers = format.IncludeSpeakers
	}

	if format.IncludeTimestamps != nil {
		converted.IncludeTimestamps = format.IncludeTimestamps
	}

	if format.SegmentOnSilenceLongerThanS != nil {
		converted.SegmentOnSilenceLongerThanS = format.SegmentOnSilenceLongerThanS
	}

	if format.MaxSegmentDurationS != nil {
		converted.MaxSegmentDurationS = format.MaxSegmentDurationS
	}

	if format.MaxSegmentChars != nil {
		converted.MaxSegmentChars = format.MaxSegmentChars
	}

	if format.MaxCharactersPerLine != nil {
		converted.MaxCharactersPerLine = format.MaxCharactersPerLine
	}

	return converted, true
}

func convertChunksToBifrost(chunks []ElevenlabsSpeechToTextChunkResponse) (string, []schemas.TranscriptionWord, []schemas.TranscriptionLogProb, *string, *float64) {
	if len(chunks) == 0 {
		return "", nil, nil, nil, nil
	}

	textParts := make([]string, 0, len(chunks))
	allWords := make([]schemas.TranscriptionWord, 0)
	allLogProbs := make([]schemas.TranscriptionLogProb, 0)

	var language *string
	var overallDuration *float64

	for _, chunk := range chunks {
		textParts = append(textParts, chunk.Text)

		words, logProbs, chunkDuration := convertWords(chunk.Words)
		allWords = append(allWords, words...)
		allLogProbs = append(allLogProbs, logProbs...)

		if language == nil && chunk.LanguageCode != "" {
			lc := chunk.LanguageCode
			language = &lc
		}

		if chunkDuration != nil {
			if overallDuration == nil || *chunkDuration > *overallDuration {
				val := *chunkDuration
				overallDuration = &val
			}
		}
	}

	return strings.Join(textParts, "\n"), allWords, allLogProbs, language, overallDuration
}

func convertWords(words []ElevenlabsSpeechToTextWord) ([]schemas.TranscriptionWord, []schemas.TranscriptionLogProb, *float64) {
	if len(words) == 0 {
		return nil, nil, nil
	}

	convertedWords := make([]schemas.TranscriptionWord, 0, len(words))
	logProbs := make([]schemas.TranscriptionLogProb, 0, len(words))

	var maxEnd float64
	var hasEnd bool

	for _, word := range words {
		trimmed := strings.TrimSpace(word.Text)
		if word.Type == "spacing" && trimmed == "" {
			continue
		}

		transcriptionWord := schemas.TranscriptionWord{
			Word: word.Text,
		}

		if word.Start != nil {
			transcriptionWord.Start = *word.Start
		}

		if word.End != nil {
			transcriptionWord.End = *word.End
			if !hasEnd || *word.End > maxEnd {
				maxEnd = *word.End
				hasEnd = true
			}
		}

		convertedWords = append(convertedWords, transcriptionWord)
		logProbs = append(logProbs, schemas.TranscriptionLogProb{
			Token:   word.Text,
			LogProb: word.LogProb,
		})
	}

	if !hasEnd {
		return convertedWords, logProbs, nil
	}

	duration := maxEnd
	return convertedWords, logProbs, &duration
}

func parseTranscriptionResponse(body []byte) ([]ElevenlabsSpeechToTextChunkResponse, error) {
	var multichannel ElevenlabsMultichannelSpeechToTextResponse
	if err := sonic.Unmarshal(body, &multichannel); err == nil && len(multichannel.Transcripts) > 0 {
		return multichannel.Transcripts, nil
	}

	var single ElevenlabsSpeechToTextChunkResponse
	if err := sonic.Unmarshal(body, &single); err == nil {
		if single.LanguageCode != "" || single.Text != "" || len(single.Words) > 0 {
			return []ElevenlabsSpeechToTextChunkResponse{single}, nil
		}
	}

	var webhook ElevenlabsSpeechToTextWebhookResponse
	if err := sonic.Unmarshal(body, &webhook); err == nil && strings.TrimSpace(webhook.Message) != "" {
		return nil, errors.New(webhook.Message)
	}

	return nil, errors.New("unexpected Elevenlabs transcription response format")
}
