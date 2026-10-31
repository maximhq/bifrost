package logstore

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// RealtimeInputCorrection identifies one input position to patch on the ordered object writer.
// It is transient and never adds a database column or an object payload field.
type RealtimeInputCorrection struct {
	Index      int    `json:"index"`
	ItemID     string `json:"item_id"`
	Transcript string `json:"transcript"`
	Raw        string `json:"raw,omitempty"`
	Summary    string `json:"-"` // DB summary written before the ordered object merge is queued
}

const realtimeInputCorrectionField = "__realtime_input_correction"

// mergeRealtimeInputCorrection patches the latest object, preserving earlier input corrections.
// Each row's uploads are ordered already, so no per-session map or waiting goroutine is needed.
func mergeRealtimeInputCorrection(base, overlay map[string]string) error {
	encoded := overlay[realtimeInputCorrectionField]
	if encoded == "" {
		return nil
	}
	delete(overlay, realtimeInputCorrectionField)
	var corrections []RealtimeInputCorrection
	if err := sonic.UnmarshalString(encoded, &corrections); err != nil {
		return err
	}
	var inputs []schemas.ChatMessage
	if base["input_history"] != "" {
		if err := sonic.UnmarshalString(base["input_history"], &inputs); err != nil {
			return err
		}
	}
	for _, correction := range corrections {
		// Excluded payload fields remain in the DB, where BatchUpdate already
		// applied the correction. An offloaded raw request may still need merging.
		if len(inputs) == 0 && overlay["input_history"] == "" {
			if base["raw_request"] != "" && correction.Raw != "" {
				base["raw_request"] = replaceRealtimeInputRaw(base["raw_request"], correction.ItemID, correction.Raw)
				overlay["raw_request"] = base["raw_request"]
			}
			continue
		}
		if correction.Index < 0 || correction.Index >= len(inputs) {
			return fmt.Errorf("realtime input position %d absent from stored object", correction.Index)
		}
		if inputs[correction.Index].Role != schemas.ChatMessageRoleUser {
			return fmt.Errorf("realtime input position %d is not a user message", correction.Index)
		}
		inputs[correction.Index].Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr(correction.Transcript)}
		if base["raw_request"] != "" && correction.Raw != "" {
			base["raw_request"] = replaceRealtimeInputRaw(base["raw_request"], correction.ItemID, correction.Raw)
			overlay["raw_request"] = base["raw_request"]
		}
	}
	if len(inputs) != 0 {
		encodedInputs, err := sonic.MarshalString(inputs)
		if err != nil {
			return err
		}
		overlay["input_history"] = encodedInputs
	}
	return nil
}

// replaceRealtimeInputRaw replaces only the event for the corrected provider input item.
func replaceRealtimeInputRaw(raw, itemID, eventRaw string) string {
	parts := strings.Split(raw, "\n\n")
	for i, part := range parts {
		event, err := schemas.ParseRealtimeEvent([]byte(part))
		if err != nil || event == nil {
			continue
		}
		id := ""
		if event.Item != nil {
			id = event.Item.ID
		}
		if id == "" {
			_ = sonic.Unmarshal(event.ExtraParams["item_id"], &id)
		}
		if id == itemID {
			parts[i] = eventRaw
		}
	}
	return strings.Join(parts, "\n\n")
}
