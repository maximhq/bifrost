package logging

import (
	"time"
)

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
	data.EmbeddingOutput = nil	
	data.Cost = nil	
	p.updateDataPool.Put(data)
}
