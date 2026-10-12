package handlers

import (
	"fmt"
	"unicode/utf8"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

const liveServerEventsPath = "session.client.data_channel.allowed_server_events"

type liveServerEventSelector struct {
	Type          schemas.LiveEventType               `json:"type"`
	ResponseEvent schemas.ResponsesStreamResponseType `json:"response_event,omitempty"`
}

// liveWebRTCEventFilter preserves the frontend's startup permissions while the gateway's
// upstream peer receives the events consumed by liveSessionController. A nil filter allows all.
type liveWebRTCEventFilter struct {
	allowed  map[liveServerEventSelector]struct{}
	original []byte
}

// prepareLiveWebRTCEvents widens only a restricted server-event list. Omission and "all"
// already deliver every event. Client command permissions and the rest of the body stay intact.
func prepareLiveWebRTCEvents(body []byte) ([]byte, *liveWebRTCEventFilter, *schemas.BifrostError) {
	value := providerUtils.GetJSONField(body, liveServerEventsPath)
	if !value.Exists() || value.Type == gjson.String && value.Str == "all" {
		return body, nil, nil
	}
	invalid := func(detail string) ([]byte, *liveWebRTCEventFilter, *schemas.BifrostError) {
		return nil, nil, newRealtimeWireBifrostError(400, "invalid_request_error", liveServerEventsPath+" "+detail)
	}
	if !value.IsArray() {
		return invalid(`must be "all" or an array of server event selectors`)
	}
	filter := &liveWebRTCEventFilter{
		allowed:  make(map[liveServerEventSelector]struct{}),
		original: []byte(value.Raw),
	}
	var selectors []liveServerEventSelector
	for i, entry := range value.Array() {
		if !entry.IsObject() {
			return invalid(fmt.Sprintf("selector %d must be an object", i))
		}
		kind, nested := entry.Get("type"), entry.Get("response_event")
		if kind.Type != gjson.String || utf8.RuneCountInString(kind.Str) < 1 || utf8.RuneCountInString(kind.Str) > 256 {
			return invalid(fmt.Sprintf("selector %d type must be a string of 1 to 256 characters", i))
		}
		selector := liveServerEventSelector{Type: schemas.LiveEventType(kind.Str)}
		if selector.Type == schemas.LiveEventResponseEvent {
			if nested.Type != gjson.String || utf8.RuneCountInString(nested.Str) < 1 || utf8.RuneCountInString(nested.Str) > 256 {
				return invalid(fmt.Sprintf("selector %d response_event must be a string of 1 to 256 characters for response.event", i))
			}
			selector.ResponseEvent = schemas.ResponsesStreamResponseType(nested.Str)
		} else if nested.Exists() {
			return invalid(fmt.Sprintf("selector %d response_event is only allowed for response.event", i))
		}
		// Unknown selector fields cannot safely be enforced at the browser boundary.
		unknown := false
		entry.ForEach(func(key, _ gjson.Result) bool {
			unknown = key.Str != "type" && key.Str != "response_event"
			return !unknown
		})
		if unknown {
			return invalid(fmt.Sprintf("selector %d only supports type and response_event", i))
		}
		filter.allowed[selector] = struct{}{}
		selectors = append(selectors, selector)
	}

	// Keep this list aligned with the events consumed by liveSessionController.fromUpstream:
	// lifecycle and policy, cumulative voice usage, transcripts and delegated response outcomes.
	required := [...]liveServerEventSelector{
		{Type: schemas.LiveEventSessionStarted},
		{Type: schemas.LiveEventSessionUpdated},
		{Type: schemas.LiveEventSessionClosed},
		{Type: schemas.LiveEventUsageUpdated},
		{Type: schemas.LiveEventDelegationCreated},
		{Type: schemas.LiveEventInputTranscriptDelta},
		{Type: schemas.LiveEventOutputTranscriptDelta},
		{Type: schemas.LiveEventError},
		{Type: schemas.LiveEventResponseEvent, ResponseEvent: schemas.ResponsesStreamResponseTypeOutputItemDone},
		{Type: schemas.LiveEventResponseEvent, ResponseEvent: schemas.ResponsesStreamResponseTypeCompleted},
		{Type: schemas.LiveEventResponseEvent, ResponseEvent: schemas.ResponsesStreamResponseTypeIncomplete},
		{Type: schemas.LiveEventResponseEvent, ResponseEvent: schemas.ResponsesStreamResponseTypeFailed},
	}
	for _, selector := range required {
		if _, allowed := filter.allowed[selector]; !allowed {
			selectors = append(selectors, selector)
		}
	}
	expanded, err := providerUtils.MarshalSorted(selectors)
	if err != nil {
		return nil, nil, newRealtimeWireBifrostError(500, "server_error", "failed to encode live server event permissions")
	}
	upstream, err := providerUtils.SetRawJSONField(body, liveServerEventsPath, expanded)
	if err != nil {
		return nil, nil, newRealtimeWireBifrostError(500, "server_error", "failed to prepare live server event permissions")
	}
	return upstream, filter, nil
}

func (f *liveWebRTCEventFilter) allows(message []byte) bool {
	if f == nil {
		return true
	}
	selector := liveServerEventSelector{Type: schemas.LiveEventTypeOf(message)}
	if selector.Type == schemas.LiveEventResponseEvent {
		selector.ResponseEvent = schemas.ResponsesStreamResponseType(providerUtils.GetJSONField(message, "event.type").Str)
	}
	_, allowed := f.allowed[selector]
	return allowed
}

// clientMessage drops events the frontend did not request and restores its original permissions
// wherever the provider echoes them. A filtered event returns nil without a transport error.
func (f *liveWebRTCEventFilter) clientMessage(message []byte) ([]byte, error) {
	if !f.allows(message) {
		return nil, nil
	}
	if f == nil || !providerUtils.JSONFieldExists(message, liveServerEventsPath) {
		return message, nil
	}
	return providerUtils.SetRawJSONField(message, liveServerEventsPath, f.original)
}
