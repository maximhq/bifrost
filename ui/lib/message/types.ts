export enum MessageType {
  CompletionRequest = 'completion_request',
  CompletionResult = 'completion_result',
  CompletionError = 'error',
  ToolResult = 'tool_result',
}

export enum MessageRole {
  ASSISTANT = 'assistant',
  USER = 'user',
  SYSTEM = 'system',
  TOOL = 'tool',
  DEVELOPER = 'developer',
}

export type ToolCallFunction = {
  name: string
  arguments: string
}

export type ToolCall = {
  type: 'function'
  id: string
  function: ToolCallFunction
}

export type MessageContent = {
  type: 'text' | 'image_url' | 'input_audio' | 'file'
  text?: string
  image_url?: MessageImageURL
  input_audio?: MessageInputAudio
  file?: MessageFile
}

export type MessageImageURL = {
  url: string
  detail?: 'auto' | 'low' | 'high'
}

export type MessageInputAudio = {
  data: string
  format: string
}

export type MessageFile = {
  file_data?: string
  file_id?: string
  filename?: string
  file_type?: string
}

export type CompletionRequest = {
  role: MessageRole
  content: string | MessageContent[] | null
  tool_call_id?: string
  tool_calls?: ToolCall[]
}

export type CompletionResultChoice = {
  index: number
  message: {
    role: MessageRole
    content: string
    tool_calls?: ToolCall[]
  }
  finish_reason?: string
}

export type CompletionResult = {
  id: string
  choices: CompletionResultChoice[]
}

export type ToolResult = {
  role: MessageRole.TOOL
  content: string
  tool_call_id: string
  isError?: boolean
}

export type MessageError = {
  code: string | number
  message: string
}

export type SerializedMessage = {
  id: string
  index: number
  originalType: MessageType
  currentType: MessageType
  payload?: CompletionRequest | CompletionResult | ToolResult
  error?: MessageError
}

export type APIMessage = {
  role: MessageRole
  content: string | MessageContent[] | null
  tool_calls?: ToolCall[]
  tool_call_id?: string
}
