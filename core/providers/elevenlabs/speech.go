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
		if bifrostReq.Params.Stability != nil {
			voiceSettings.Stability = *bifrostReq.Params.Stability
			hasVoiceSettings = true
		}
		if bifrostReq.Params.UseSpeakerBoost != nil {
			voiceSettings.UseSpeakerBoost = *bifrostReq.Params.UseSpeakerBoost
			hasVoiceSettings = true
		}
		if bifrostReq.Params.SimilarityBoost != nil {
			voiceSettings.SimilarityBoost = *bifrostReq.Params.SimilarityBoost
			hasVoiceSettings = true
		}
		if bifrostReq.Params.Style != nil {
			voiceSettings.Style = *bifrostReq.Params.Style
			hasVoiceSettings = true
		}
		if hasVoiceSettings {
			elevenlabsReq.VoiceSettings = &voiceSettings
		}

		if bifrostReq.Params.LanguageCode != nil {
			elevenlabsReq.LanguageCode = bifrostReq.Params.LanguageCode
		}

		if bifrostReq.Params.Seed != nil {
			elevenlabsReq.Seed = bifrostReq.Params.Seed
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

		if bifrostReq.Params.PreviousText != nil {
			elevenlabsReq.PreviousText = bifrostReq.Params.PreviousText
		}

		if bifrostReq.Params.NextText != nil {
			elevenlabsReq.NextText = bifrostReq.Params.NextText
		}

		if bifrostReq.Params.PreviousRequestIDs != nil {
			elevenlabsReq.PreviousRequestIDs = bifrostReq.Params.PreviousRequestIDs
		}

		if bifrostReq.Params.NextRequestIDs != nil {
			elevenlabsReq.NextRequestIDs = bifrostReq.Params.NextRequestIDs
		}

		if bifrostReq.Params.ApplyTextNormalization != nil {
			tn := TextNormalization(*bifrostReq.Params.ApplyTextNormalization)
			elevenlabsReq.ApplyTextNormalization = &tn
		}

		if bifrostReq.Params.ApplyLanguageTextNormalization != nil {
			elevenlabsReq.ApplyLanguageTextNormalization = bifrostReq.Params.ApplyLanguageTextNormalization
		}

		if bifrostReq.Params.UsePVCAsIVC != nil {
			elevenlabsReq.UsePVCAsIVC = bifrostReq.Params.UsePVCAsIVC
		}
	}

	return elevenlabsReq
}
