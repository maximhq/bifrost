package logging

import (
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
)

// stripUnserializablePayloads clears the parsed payload fields of a log entry
// that fail JSON serialization, so the row's scalar columns (id, model, cost,
// tokens, status) can still be inserted after a marshal error aborted the
// normal write. Only fields that actually fail to marshal are cleared; the
// rest of the payload is preserved.
func stripUnserializablePayloads(entry *logstore.Log) {
	if entry == nil {
		return
	}
	if !marshals(entry.InputHistoryParsed) {
		entry.InputHistoryParsed = nil
	}
	if !marshals(entry.ResponsesInputHistoryParsed) {
		entry.ResponsesInputHistoryParsed = nil
	}
	if !marshals(entry.OutputMessageParsed) {
		entry.OutputMessageParsed = nil
	}
	if !marshals(entry.ResponsesOutputParsed) {
		entry.ResponsesOutputParsed = nil
	}
	if !marshals(entry.ToolCallsParsed) {
		entry.ToolCallsParsed = nil
	}
	if !marshals(entry.ParamsParsed) {
		entry.ParamsParsed = nil
	}
	if !marshals(entry.ErrorDetailsParsed) {
		entry.ErrorDetailsParsed = nil
	}
}

// marshals reports whether v serializes cleanly to JSON; nil values trivially do.
func marshals(v interface{}) bool {
	if v == nil {
		return true
	}
	_, err := sonic.Marshal(v)
	return err == nil
}
