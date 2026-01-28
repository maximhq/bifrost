package elevenlabs

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ToElevenlabsSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *ElevenlabsSpeechRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	elevenlabsReq := &ElevenlabsSpeechRequest{
		ModelID: bifrostReq.Model,
		Text:    bifrostReq.Input.Input,
	}

	if bifrostReq.Params != nil {
		voiceSettings := ElevenlabsVoiceSettings{}
		hasVoiceSettings := false

		if bifrostReq.Params.Speed != nil {
			voiceSettings.Speed = *bifrostReq.Params.Speed
			hasVoiceSettings = true
		}

		if bifrostReq.Params.ExtraParams != nil {
			if stability, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["stability"]); ok {
				voiceSettings.Stability = *stability
				hasVoiceSettings = true
			}
			if useSpeakerBoost, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["use_speaker_boost"]); ok {
				voiceSettings.UseSpeakerBoost = *useSpeakerBoost
				hasVoiceSettings = true
			}
			if similarityBoost, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["similarity_boost"]); ok {
				voiceSettings.SimilarityBoost = *similarityBoost
				hasVoiceSettings = true
			}
			if style, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["style"]); ok {
				voiceSettings.Style = *style
				hasVoiceSettings = true
			}
			if seed, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["seed"]); ok {
				elevenlabsReq.Seed = seed
			}
			if previousText, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["previous_text"]); ok {
				elevenlabsReq.PreviousText = previousText
			}
			if nextText, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["next_text"]); ok {
				elevenlabsReq.NextText = nextText
			}
			if previousRequestIDs, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["previous_request_ids"]); ok {
				elevenlabsReq.PreviousRequestIDs = previousRequestIDs
			}
			if nextRequestIDs, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["next_request_ids"]); ok {
				elevenlabsReq.NextRequestIDs = nextRequestIDs
			}
			if applyTextNormalization, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["apply_text_normalization"]); ok {
				elevenlabsReq.ApplyTextNormalization = applyTextNormalization
			}
			if applyLanguageTextNormalization, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["apply_language_text_normalization"]); ok {
				elevenlabsReq.ApplyLanguageTextNormalization = applyLanguageTextNormalization
			}
			if usePVCAsIVC, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["use_pvc_as_ivc"]); ok {
				elevenlabsReq.UsePVCAsIVC = usePVCAsIVC
			}
		}

		if hasVoiceSettings {
			elevenlabsReq.VoiceSettings = &voiceSettings
		}

		if bifrostReq.Params.LanguageCode != nil {
			elevenlabsReq.LanguageCode = bifrostReq.Params.LanguageCode
		}

		if len(bifrostReq.Params.PronunciationDictionaryLocators) > 0 {
			elevenlabsReq.PronunciationDictionaryLocators = make([]ElevenlabsPronunciationDictionaryLocator, len(bifrostReq.Params.PronunciationDictionaryLocators))
			for i, locator := range bifrostReq.Params.PronunciationDictionaryLocators {
				elevenlabsReq.PronunciationDictionaryLocators[i] = ElevenlabsPronunciationDictionaryLocator{
					PronunciationDictionaryID: locator.PronunciationDictionaryID,
					VersionID:                 locator.VersionID,
				}
			}
		}
	}

	return elevenlabsReq
}
