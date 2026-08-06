package munsit

import (
	"strings"

	"github.com/bytedance/sonic"
)

// SPEECH TYPES

type MunsitSpeechRequest struct {
	VoiceID				    		string                                     `json:"voice_id"`
	Text                            string                                     `json:"text"`
	Stability 						float64 								   `json:"stability,omitempty"`
    Speed     						float64 								   `json:"speed,omitempty"`
    Streaming 						bool    								   `json:"streaming,omitempty"`

	ExtraParams                     map[string]interface{}                     `json:"-"`
}

// GetExtraParams implements the providerUtils.RequestBodyWithExtraParams interface.
func (r *MunsitSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MunsitSpeechWithTimestampsResponse represents the response from the with-timestamps endpoint
type MunsitSpeechWithTimestampsResponse struct {
	AudioBase64         string               `json:"audio_base64"`
	Alignment           *MunsitAlignment `json:"alignment,omitempty"`
	NormalizedAlignment *MunsitAlignment `json:"normalized_alignment,omitempty"`
}

// MunsitAlignment represents character-level timing information
type MunsitAlignment struct {
	CharStartTimesMs []float64 `json:"char_start_times_ms"`
	CharEndTimesMs   []float64 `json:"char_end_times_ms"`
	Characters       []string  `json:"characters"`
}


// TRANSCRIPTION TYPES
type MunsitTranscriptionRequest struct {
	File               []byte `form:"file"`
	Filename           string

	Model              string `form:"model"`

	ReturnTimestamps   bool `form:"return_timestamps,omitempty"`
	ReturnConfidence   bool `form:"return_confidence,omitempty"`
	ReturnTurns        bool `form:"return_turns,omitempty"`
	ReturnGender       bool `form:"return_gender,omitempty"`
	ReturnSentiment    bool `form:"return_sentiment,omitempty"`
}

type MunsitTranscriptionResponse struct {
    StatusCode int    `json:"statusCode"`
    Message    string `json:"message"`
    Data       MunsitTranscriptionData `json:"data"`
}

type MunsitTranscriptionData struct {
    TranscriptionID string             `json:"transcriptionId"`
    Transcription   string             `json:"transcription"`
    Duration        float64            `json:"duration"`
    Timestamps      []MunsitTimestamp  `json:"timestamps"`
    AudioURL        string             `json:"audioUrl"`
    Attributes      MunsitAttributes   `json:"attributes"`
}

type MunsitAttributes struct {
	TimestampsRaw []MunsitTimestamp `json:"timestampsRaw"`
}

type MunsitTimestamp struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}


type MunsitAdditionalFormatResponse struct {
	RequestedFormat string `json:"requested_format"`
	FileExtension   string `json:"file_extension"`
	ContentType     string `json:"content_type"`
	IsBase64Encoded bool   `json:"is_base64_encoded"`
	Content         string `json:"content"`
}


// ERROR TYPES
type MunsitError struct {
	Detail *MunsitErrorDetail `json:"detail,omitempty"`
}

// MunsitErrorDetail handles both single object (non-validation errors) and
// array of objects (validation errors) formats from ElevenLabs API.
type MunsitErrorDetail struct {
	// Non-validation error fields (when detail is a single object)
	Status  *string `json:"status,omitempty"`
	Message *string `json:"message,omitempty"`

	// Validation error fields (when detail is an array)
	ValidationErrors []MunsitValidationError `json:"-"`
}

// MunsitValidationError represents a single validation error entry
type MunsitValidationError struct {
	Loc     []string `json:"loc"`
	Msg     string   `json:"msg"`
	Message string   `json:"message"` // Some APIs use "message" instead of "msg"
	Type    string   `json:"type"`
}

// UnmarshalJSON implements custom JSON unmarshaling to handle both
// single object and array formats from ElevenLabs API.
func (d *MunsitErrorDetail) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as an array (validation errors)
	// Check if it's an array by looking at the first non-whitespace character
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var validationErrors []MunsitValidationError
		if err := sonic.Unmarshal(data, &validationErrors); err != nil {
			return err
		}
		d.ValidationErrors = validationErrors
		// Extract message from first validation error if available
		if len(validationErrors) > 0 {
			if validationErrors[0].Message != "" {
				d.Message = &validationErrors[0].Message
			} else if validationErrors[0].Msg != "" {
				d.Message = &validationErrors[0].Msg
			}
		}
		return nil
	}

	// If not an array, try to unmarshal as a single object (non-validation error)
	var obj struct {
		Type    *string  `json:"type,omitempty"`
		Loc     []string `json:"loc,omitempty"`
		Message *string  `json:"message,omitempty"`
		Status  *string  `json:"status,omitempty"`
		Msg     *string  `json:"msg,omitempty"` // Some APIs use "msg" instead of "message"
	}
	if err := sonic.Unmarshal(data, &obj); err != nil {
		return err
	}

	// Populate non-validation error fields
	d.Status = obj.Status
	if obj.Message != nil {
		d.Message = obj.Message
	} else if obj.Msg != nil {
		d.Message = obj.Msg
	}

	// If this object has validation-like fields (Loc, Type), treat it as a single validation error
	if len(obj.Loc) > 0 || obj.Type != nil {
		validationErr := MunsitValidationError{
			Loc: obj.Loc,
			Type: func() string {
				if obj.Type != nil {
					return *obj.Type
				}
				return ""
			}(),
		}
		if obj.Message != nil {
			validationErr.Message = *obj.Message
		} else if obj.Msg != nil {
			validationErr.Msg = *obj.Msg
			validationErr.Message = *obj.Msg
		}
		d.ValidationErrors = []MunsitValidationError{validationErr}
	}

	return nil
}

// MODEL TYPES
type MunsitModel struct {
	ID								   string               `json:"id"`
	ModelID                            string               `json:"model_id"`
	ModelName                          string               `json:"model_name"`
	Description                        string               `json:"description"`
}

type MunsitListModelsResponse []MunsitModel
