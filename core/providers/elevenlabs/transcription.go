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

	if params.ExtraParams != nil {
		if tagAudioEvents, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["tag_audio_events"]); ok {
			delete(params.ExtraParams, "tag_audio_events")
			req.TagAudioEvents = tagAudioEvents
		}
		if numSpeakers, ok := schemas.SafeExtractIntPointer(params.ExtraParams["num_speakers"]); ok {
			delete(params.ExtraParams, "num_speakers")
			req.NumSpeakers = numSpeakers
		}
		if timestampsGranularity, ok := schemas.SafeExtractStringPointer(params.ExtraParams["timestamps_granularity"]); ok {
			granularity := ElevenlabsTimestampsGranularity(*timestampsGranularity)
			delete(params.ExtraParams, "timestamps_granularity")
			req.TimestampsGranularity = &granularity
		}
		if diarize, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["diarize"]); ok {
			delete(params.ExtraParams, "diarize")
			req.Diarize = diarize
		}
		if diarizationThreshold, ok := schemas.SafeExtractFloat64Pointer(params.ExtraParams["diarization_threshold"]); ok {
			delete(params.ExtraParams, "diarization_threshold")
			req.DiarizationThreshold = diarizationThreshold
		}
		if fileFormat, ok := schemas.SafeExtractStringPointer(params.ExtraParams["file_format"]); ok {
			fileFormat := ElevenlabsFileFormat(*fileFormat)
			delete(params.ExtraParams, "file_format")
			req.FileFormat = &fileFormat
		}
		if cloudStorageURL, ok := schemas.SafeExtractStringPointer(params.ExtraParams["cloud_storage_url"]); ok {
			delete(params.ExtraParams, "cloud_storage_url")
			req.CloudStorageURL = cloudStorageURL
		}
		if webhook, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["webhook"]); ok {
			delete(params.ExtraParams, "webhook")
			req.Webhook = webhook
		}
		if webhookID, ok := schemas.SafeExtractStringPointer(params.ExtraParams["webhook_id"]); ok {
			delete(params.ExtraParams, "webhook_id")
			req.WebhookID = webhookID
		}
		if temperature, ok := schemas.SafeExtractFloat64Pointer(params.ExtraParams["temperature"]); ok {
			delete(params.ExtraParams, "temperature")
			req.Temperature = temperature
		}
		if seed, ok := schemas.SafeExtractIntPointer(params.ExtraParams["seed"]); ok {
			delete(params.ExtraParams, "seed")
			req.Seed = seed
		}
		if useMultiChannel, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["use_multi_channel"]); ok {
			delete(params.ExtraParams, "use_multi_channel")
			req.UseMultiChannel = useMultiChannel
		}
		req.ExtraParams = bifrostReq.Params.ExtraParams
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

func ToBifrostTranscriptionResponse(chunks []ElevenlabsSpeechToTextChunkResponse) *schemas.BifrostTranscriptionResponse {
	if len(chunks) == 0 {
		return nil
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

	text := strings.Join(textParts, "\n")

	response := &schemas.BifrostTranscriptionResponse{
		Text:     text,
		Words:    allWords,
		LogProbs: allLogProbs,
	}

	if language != nil {
		response.Language = language
	}

	if overallDuration != nil {
		response.Duration = overallDuration
	}

	return response

}

func convertAdditionalFormat(format schemas.TranscriptionAdditionalFormat) (ElevenlabsAdditionalFormat, bool) {
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
