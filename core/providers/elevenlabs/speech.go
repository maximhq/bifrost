package elevenlabs

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostSpeechRequest converts the text-to-speech body an ElevenLabs client sent, and the voice its path names, to a
// Bifrost request. The call goes upstream with the body as the client wrote it, so only what governance, logs and
// cost read is converted: the model, the text, the voice and the language. A model with no provider prefix leaves the
// provider to the model catalog, as for every other route.
func (r *ElevenlabsSpeechRequest) ToBifrostSpeechRequest(voice string) *schemas.BifrostSpeechRequest {
	provider, model := schemas.ParseModelString(r.ModelID, "")
	return &schemas.BifrostSpeechRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.SpeechInput{Input: r.Text},
		Params: &schemas.SpeechParameters{
			VoiceConfig:  &schemas.SpeechVoiceInput{Voice: &voice},
			LanguageCode: r.LanguageCode,
		},
	}
}

func ToElevenlabsSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *ElevenlabsSpeechRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	elevenlabsReq := &ElevenlabsSpeechRequest{
		ModelID: bifrostReq.Model,
		Text:    bifrostReq.Input.Input,
	}

	if bifrostReq.Params != nil {
		elevenlabsReq.ExtraParams = bifrostReq.Params.ExtraParams
		voiceSettings := ElevenlabsVoiceSettings{}
		hasVoiceSettings := false

		if bifrostReq.Params.Speed != nil {
			voiceSettings.Speed = bifrostReq.Params.Speed
			hasVoiceSettings = true
		}

		if bifrostReq.Params.ExtraParams != nil {
			if stability, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["stability"]); ok {
				delete(elevenlabsReq.ExtraParams, "stability")
				voiceSettings.Stability = stability
				hasVoiceSettings = true
			}
			if useSpeakerBoost, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["use_speaker_boost"]); ok {
				delete(elevenlabsReq.ExtraParams, "use_speaker_boost")
				voiceSettings.UseSpeakerBoost = useSpeakerBoost
				hasVoiceSettings = true
			}
			if similarityBoost, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["similarity_boost"]); ok {
				delete(elevenlabsReq.ExtraParams, "similarity_boost")
				voiceSettings.SimilarityBoost = similarityBoost
				hasVoiceSettings = true
			}
			if style, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["style"]); ok {
				delete(elevenlabsReq.ExtraParams, "style")
				voiceSettings.Style = style
				hasVoiceSettings = true
			}
			if seed, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["seed"]); ok {
				delete(elevenlabsReq.ExtraParams, "seed")
				elevenlabsReq.Seed = seed
			}
			if previousText, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["previous_text"]); ok {
				delete(elevenlabsReq.ExtraParams, "previous_text")
				elevenlabsReq.PreviousText = previousText
			}
			if nextText, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["next_text"]); ok {
				delete(elevenlabsReq.ExtraParams, "next_text")
				elevenlabsReq.NextText = nextText
			}
			if previousRequestIDs, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["previous_request_ids"]); ok {
				delete(elevenlabsReq.ExtraParams, "previous_request_ids")
				elevenlabsReq.PreviousRequestIDs = previousRequestIDs
			}
			if nextRequestIDs, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["next_request_ids"]); ok {
				delete(elevenlabsReq.ExtraParams, "next_request_ids")
				elevenlabsReq.NextRequestIDs = nextRequestIDs
			}
			if applyTextNormalization, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["apply_text_normalization"]); ok {
				delete(elevenlabsReq.ExtraParams, "apply_text_normalization")
				elevenlabsReq.ApplyTextNormalization = applyTextNormalization
			}
			if applyLanguageTextNormalization, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["apply_language_text_normalization"]); ok {
				delete(elevenlabsReq.ExtraParams, "apply_language_text_normalization")
				elevenlabsReq.ApplyLanguageTextNormalization = applyLanguageTextNormalization
			}
			if usePVCAsIVC, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["use_pvc_as_ivc"]); ok {
				delete(elevenlabsReq.ExtraParams, "use_pvc_as_ivc")
				elevenlabsReq.UsePVCAsIVC = usePVCAsIVC
			}
		}

		if hasVoiceSettings {
			elevenlabsReq.VoiceSettings = &voiceSettings
		}

		if bifrostReq.Params.LanguageCode != nil {
			elevenlabsReq.LanguageCode = bifrostReq.Params.LanguageCode
		}

		elevenlabsReq.PronunciationDictionaryLocators = toElevenlabsPronunciationDictionaryLocators(bifrostReq.Params.PronunciationDictionaryLocators)
	}

	return elevenlabsReq
}

// toElevenlabsPronunciationDictionaryLocators converts the request's locators, and returns nil for none.
func toElevenlabsPronunciationDictionaryLocators(locators []schemas.SpeechPronunciationDictionaryLocator) []ElevenlabsPronunciationDictionaryLocator {
	if len(locators) == 0 {
		return nil
	}
	converted := make([]ElevenlabsPronunciationDictionaryLocator, len(locators))
	for i, locator := range locators {
		converted[i] = ElevenlabsPronunciationDictionaryLocator{
			PronunciationDictionaryID: locator.PronunciationDictionaryID,
			VersionID:                 locator.VersionID,
		}
	}
	return converted
}
