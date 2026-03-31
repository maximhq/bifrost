export { Message } from './message'
export {
  MessageRole,
  MessageType,
  type APIMessage,
  type CompletionRequest,
  type CompletionResult,
  type CompletionResultChoice,
  type CompletionUsage,
  type MessageContent,
  type MessageError,
  type MessageFile,
  type MessageImageURL,
  type MessageInputAudio,
  type SerializedMessage,
  type ToolCall,
  type ToolCallFunction,
  type ToolResult,
} from './types'
export {
  extractVariablesFromText,
  extractVariablesFromMessages,
  replaceVariablesInText,
  replaceVariablesInMessages,
  mergeVariables,
  type VariableMap,
} from './variables'
