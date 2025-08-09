package logging

import "time"

// getLogMessage gets a LogMessage from the pool
func (p *LoggerPlugin) getLogMessage() *LogMessage {
	return p.logMsgPool.Get().(*LogMessage)
}

// putLogMessage returns a LogMessage to the pool after resetting it
func (p *LoggerPlugin) putLogMessage(msg *LogMessage) {
	// Reset the message fields to avoid memory leaks
	msg.Operation = ""
	msg.RequestID = ""
	msg.Timestamp = time.Time{}
	msg.InitialData = nil

	// Don't reset UpdateData and StreamUpdateData here since they're returned
	// to their own pools in the defer function - just clear the pointers
	msg.UpdateData = nil
	msg.StreamUpdateData = nil

	p.logMsgPool.Put(msg)
}

// getUpdateLogData gets an UpdateLogData from the pool
func (p *LoggerPlugin) getUpdateLogData() *UpdateLogData {
	return p.updateDataPool.Get().(*UpdateLogData)
}

// putUpdateLogData returns an UpdateLogData to the pool after resetting it
func (p *LoggerPlugin) putUpdateLogData(data *UpdateLogData) {
	// Reset all fields to avoid memory leaks
	data.Status = ""
	data.TokenUsage = nil
	data.OutputMessage = nil
	data.ToolCalls = nil
	data.ErrorDetails = nil
	data.Model = ""
	data.Object = ""
	data.SpeechOutput = nil
	data.TranscriptionOutput = nil

	p.updateDataPool.Put(data)
}

// getStreamUpdateData gets a StreamUpdateData from the pool
func (p *LoggerPlugin) getStreamUpdateData() *StreamUpdateData {
	return p.streamDataPool.Get().(*StreamUpdateData)
}

// putStreamUpdateData returns a StreamUpdateData to the pool after resetting it
func (p *LoggerPlugin) putStreamUpdateData(data *StreamUpdateData) {
	// Reset all fields to avoid memory leaks
	data.ErrorDetails = nil
	data.Model = ""
	data.Object = ""
	data.TokenUsage = nil
	data.Delta = nil
	data.FinishReason = nil
	data.TranscriptionOutput = nil

	p.streamDataPool.Put(data)
}

// getStreamChunk gets a StreamChunk from the pool
func (p *LoggerPlugin) getStreamChunk() *StreamChunk {
	return p.streamChunkPool.Get().(*StreamChunk)
}

// putStreamChunk returns a StreamChunk to the pool after resetting it
func (p *LoggerPlugin) putStreamChunk(chunk *StreamChunk) {
	// Reset all fields to avoid memory leaks
	chunk.Timestamp = time.Time{}
	chunk.Delta = nil
	chunk.FinishReason = nil
	chunk.TokenUsage = nil
	chunk.ErrorDetails = nil

	p.streamChunkPool.Put(chunk)
}
