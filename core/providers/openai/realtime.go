package openai

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SupportsRealtimeAPI returns true since OpenAI natively supports the Realtime API.
func (provider *OpenAIProvider) SupportsRealtimeAPI() bool {
	return true
}

// RealtimeWebSocketURL returns the WSS URL for the OpenAI Realtime API.
// Format: wss://api.openai.com/v1/realtime?model=<model>
func (provider *OpenAIProvider) RealtimeWebSocketURL(key schemas.Key, model string) string {
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return base + "/v1/realtime?model=" + url.QueryEscape(model)
}

// RealtimeHeaders returns the headers required for the OpenAI Realtime WebSocket connection.
func (provider *OpenAIProvider) RealtimeHeaders(key schemas.Key) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + key.Value.GetValue(),
		"OpenAI-Beta":   "realtime=v1",
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		headers[k] = v
	}
	return headers
}

// openAIRealtimeEvent is the raw shape of an OpenAI Realtime protocol event.
type openAIRealtimeEvent struct {
	Type         string          `json:"type"`
	EventID      string          `json:"event_id,omitempty"`
	Session      json.RawMessage `json:"session,omitempty"`
	Conversation json.RawMessage `json:"conversation,omitempty"`
	Item         json.RawMessage `json:"item,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`
	Delta        string          `json:"delta,omitempty"`
	Audio        string          `json:"audio,omitempty"`
	Transcript   string          `json:"transcript,omitempty"`
	Text         string          `json:"text,omitempty"`
	Error        json.RawMessage `json:"error,omitempty"`
	ItemID       string          `json:"item_id,omitempty"`
	OutputIndex  int             `json:"output_index,omitempty"`
	ContentIndex int             `json:"content_index,omitempty"`
	ResponseID   string          `json:"response_id,omitempty"`

	PreviousItemID string `json:"previous_item_id,omitempty"`
}

// openAIRealtimeSession is the session object within an OpenAI Realtime event.
type openAIRealtimeSession struct {
	ID               string          `json:"id,omitempty"`
	Model            string          `json:"model,omitempty"`
	Modalities       []string        `json:"modalities,omitempty"`
	Instructions     string          `json:"instructions,omitempty"`
	Voice            string          `json:"voice,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	MaxOutputTokens  json.RawMessage `json:"max_output_tokens,omitempty"`
	TurnDetection    json.RawMessage `json:"turn_detection,omitempty"`
	InputAudioFormat string          `json:"input_audio_format,omitempty"`
	OutputAudioType  string          `json:"output_audio_type,omitempty"`
	Tools            json.RawMessage `json:"tools,omitempty"`
}

// openAIRealtimeItem is the item object within an OpenAI Realtime event.
type openAIRealtimeItem struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Status    string          `json:"status,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Name      string          `json:"name,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    string          `json:"output,omitempty"`
}

// openAIRealtimeError is the error object within an OpenAI Realtime event.
type openAIRealtimeError struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Param   string `json:"param,omitempty"`
}

// ToBifrostRealtimeEvent converts an OpenAI Realtime event (raw JSON) to the unified Bifrost format.
func (provider *OpenAIProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var raw openAIRealtimeEvent
	if err := json.Unmarshal(providerEvent, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OpenAI realtime event: %w", err)
	}

	event := &schemas.BifrostRealtimeEvent{
		Type:    schemas.RealtimeEventType(raw.Type),
		EventID: raw.EventID,
		RawData: providerEvent,
	}

	switch {
	case raw.Session != nil:
		var sess openAIRealtimeSession
		if err := json.Unmarshal(raw.Session, &sess); err == nil {
			event.Session = &schemas.RealtimeSession{
				ID:               sess.ID,
				Model:            sess.Model,
				Modalities:       sess.Modalities,
				Instructions:     sess.Instructions,
				Voice:            sess.Voice,
				Temperature:      sess.Temperature,
				MaxOutputTokens:  sess.MaxOutputTokens,
				TurnDetection:    sess.TurnDetection,
				InputAudioFormat: sess.InputAudioFormat,
				OutputAudioType:  sess.OutputAudioType,
				Tools:            sess.Tools,
			}
		}

	case raw.Item != nil:
		var item openAIRealtimeItem
		if err := json.Unmarshal(raw.Item, &item); err == nil {
			event.Item = &schemas.RealtimeItem{
				ID:        item.ID,
				Type:      item.Type,
				Role:      item.Role,
				Status:    item.Status,
				Content:   item.Content,
				Name:      item.Name,
				CallID:    item.CallID,
				Arguments: item.Arguments,
				Output:    item.Output,
			}
		}

	case raw.Error != nil:
		var rtErr openAIRealtimeError
		if err := json.Unmarshal(raw.Error, &rtErr); err == nil {
			event.Error = &schemas.RealtimeError{
				Type:    rtErr.Type,
				Code:    rtErr.Code,
				Message: rtErr.Message,
				Param:   rtErr.Param,
			}
		}
	}

	if isRealtimeDeltaEvent(raw.Type) {
		event.Delta = &schemas.RealtimeDelta{
			Text:       raw.Text,
			Audio:      raw.Audio,
			Transcript: raw.Transcript,
			ItemID:     raw.ItemID,
			OutputIdx:  raw.OutputIndex,
			ContentIdx: raw.ContentIndex,
			ResponseID: raw.ResponseID,
		}
		if raw.Delta != "" {
			if event.Delta.Text == "" {
				event.Delta.Text = raw.Delta
			}
		}
	}

	return event, nil
}

// ToProviderRealtimeEvent converts a unified Bifrost Realtime event back to OpenAI's native JSON.
func (provider *OpenAIProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	if bifrostEvent.RawData != nil {
		return bifrostEvent.RawData, nil
	}

	out := map[string]interface{}{
		"type": string(bifrostEvent.Type),
	}
	if bifrostEvent.EventID != "" {
		out["event_id"] = bifrostEvent.EventID
	}

	if bifrostEvent.Session != nil {
		sess := map[string]interface{}{}
		if bifrostEvent.Session.Model != "" {
			sess["model"] = bifrostEvent.Session.Model
		}
		if len(bifrostEvent.Session.Modalities) > 0 {
			sess["modalities"] = bifrostEvent.Session.Modalities
		}
		if bifrostEvent.Session.Instructions != "" {
			sess["instructions"] = bifrostEvent.Session.Instructions
		}
		if bifrostEvent.Session.Voice != "" {
			sess["voice"] = bifrostEvent.Session.Voice
		}
		if bifrostEvent.Session.Temperature != nil {
			sess["temperature"] = *bifrostEvent.Session.Temperature
		}
		if bifrostEvent.Session.MaxOutputTokens != nil {
			sess["max_output_tokens"] = bifrostEvent.Session.MaxOutputTokens
		}
		if bifrostEvent.Session.TurnDetection != nil {
			sess["turn_detection"] = bifrostEvent.Session.TurnDetection
		}
		if bifrostEvent.Session.InputAudioFormat != "" {
			sess["input_audio_format"] = bifrostEvent.Session.InputAudioFormat
		}
		if bifrostEvent.Session.OutputAudioType != "" {
			sess["output_audio_type"] = bifrostEvent.Session.OutputAudioType
		}
		if bifrostEvent.Session.Tools != nil {
			sess["tools"] = bifrostEvent.Session.Tools
		}
		out["session"] = sess
	}

	if bifrostEvent.Item != nil {
		item := map[string]interface{}{
			"type": bifrostEvent.Item.Type,
		}
		if bifrostEvent.Item.ID != "" {
			item["id"] = bifrostEvent.Item.ID
		}
		if bifrostEvent.Item.Role != "" {
			item["role"] = bifrostEvent.Item.Role
		}
		if bifrostEvent.Item.Content != nil {
			item["content"] = bifrostEvent.Item.Content
		}
		if bifrostEvent.Item.Name != "" {
			item["name"] = bifrostEvent.Item.Name
		}
		if bifrostEvent.Item.CallID != "" {
			item["call_id"] = bifrostEvent.Item.CallID
		}
		if bifrostEvent.Item.Arguments != "" {
			item["arguments"] = bifrostEvent.Item.Arguments
		}
		if bifrostEvent.Item.Output != "" {
			item["output"] = bifrostEvent.Item.Output
		}
		out["item"] = item
	}

	if bifrostEvent.Delta != nil {
		if bifrostEvent.Delta.Text != "" {
			out["delta"] = bifrostEvent.Delta.Text
		}
		if bifrostEvent.Delta.Audio != "" {
			out["audio"] = bifrostEvent.Delta.Audio
		}
		if bifrostEvent.Delta.Transcript != "" {
			out["transcript"] = bifrostEvent.Delta.Transcript
		}
		if bifrostEvent.Delta.ItemID != "" {
			out["item_id"] = bifrostEvent.Delta.ItemID
		}
		if bifrostEvent.Delta.OutputIdx != 0 {
			out["output_index"] = bifrostEvent.Delta.OutputIdx
		}
		if bifrostEvent.Delta.ContentIdx != 0 {
			out["content_index"] = bifrostEvent.Delta.ContentIdx
		}
		if bifrostEvent.Delta.ResponseID != "" {
			out["response_id"] = bifrostEvent.Delta.ResponseID
		}
	}

	return json.Marshal(out)
}

func isRealtimeDeltaEvent(eventType string) bool {
	switch eventType {
	case "response.text.delta",
		"response.audio.delta",
		"response.audio_transcript.delta",
		"conversation.item.input_audio_transcription.delta":
		return true
	}
	return false
}
