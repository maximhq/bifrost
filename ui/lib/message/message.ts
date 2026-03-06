import { v4 as uuidv4 } from 'uuid'
import {
  APIMessage,
  CompletionRequest,
  CompletionResult,
  MessageContent,
  MessageError,
  MessageRole,
  MessageType,
  SerializedMessage,
  ToolCall,
  ToolResult,
} from './types'

export class Message {
  readonly id: string
  readonly index: number
  private readonly originalType: MessageType
  private currentType: MessageType
  private _payload?: CompletionRequest | CompletionResult | ToolResult
  readonly error?: MessageError

  constructor(
    id: string,
    index: number,
    type: MessageType,
    payload?: CompletionRequest | CompletionResult | ToolResult,
    error?: MessageError
  ) {
    this.id = id
    this.index = index
    this.originalType = type
    this.currentType = type
    this._payload = payload
    this.error = error
  }

  // Convenience factory methods

  static system(content: string, index = 0): Message {
    return new Message(uuidv4(), index, MessageType.CompletionRequest, {
      role: MessageRole.SYSTEM,
      content,
    } as CompletionRequest)
  }

  static request(content: string, index = 0, attachments?: MessageContent[]): Message {
    if (attachments && attachments.length > 0) {
      const parts: MessageContent[] = [{ type: 'text', text: content }, ...attachments]
      return new Message(uuidv4(), index, MessageType.CompletionRequest, {
        role: MessageRole.USER,
        content: parts,
      } as CompletionRequest)
    }
    return new Message(uuidv4(), index, MessageType.CompletionRequest, {
      role: MessageRole.USER,
      content,
    } as CompletionRequest)
  }

  static response(content: string, index = 0): Message {
    return new Message(uuidv4(), index, MessageType.CompletionResult, {
      id: uuidv4(),
      choices: [{ index: 0, message: { role: MessageRole.ASSISTANT, content } }],
    } as CompletionResult)
  }

  static error(content: string, index = 0): Message {
    return new Message(uuidv4(), index, MessageType.CompletionError, undefined, {
      code: 'error',
      message: content,
    })
  }

  public get payload() {
    return this._payload
  }

  public get type(): MessageType {
    return this.currentType
  }

  // Serialization

  public get serialized(): SerializedMessage {
    const s: SerializedMessage = {
      id: this.id,
      index: this.index,
      originalType: this.originalType,
      currentType: this.currentType,
      payload: this._payload,
    }
    if (this.error) {
      s.error = this.error
    }
    return s
  }

  public static deserialize(serialized: SerializedMessage): Message {
    const message = new Message(
      serialized.id,
      serialized.index,
      serialized.originalType,
      serialized.payload ? JSON.parse(JSON.stringify(serialized.payload)) : undefined,
      serialized.error
    )
    message.currentType = serialized.currentType
    return message
  }

  public clone(): Message {
    return Message.deserialize(this.serialized)
  }

  // Role

  public get role(): MessageRole | undefined {
    switch (this.originalType) {
      case MessageType.CompletionRequest:
        return (this._payload as CompletionRequest)?.role
      case MessageType.CompletionResult:
        return (this._payload as CompletionResult)?.choices?.[0]?.message?.role
      case MessageType.CompletionError:
        return MessageRole.ASSISTANT
      case MessageType.ToolResult:
        return MessageRole.TOOL
      default:
        return undefined
    }
  }

  public set role(role: MessageRole) {
    switch (this.originalType) {
      case MessageType.CompletionRequest:
        (this._payload as CompletionRequest).role = role
        break
      case MessageType.CompletionResult:
        ;(this._payload as CompletionResult).choices?.forEach((choice) => {
          choice.message.role = role
        })
        break
    }
    switch (role) {
      case MessageRole.ASSISTANT:
        this.currentType = MessageType.CompletionResult
        break
      case MessageRole.USER:
      case MessageRole.SYSTEM:
      case MessageRole.DEVELOPER:
        this.currentType = MessageType.CompletionRequest
        break
      case MessageRole.TOOL:
        this.currentType = MessageType.ToolResult
        break
      default:
        this.currentType = MessageType.CompletionResult
        break
    }
  }

  // Content

  public get content(): string {
    switch (this.originalType) {
      case MessageType.CompletionRequest: {
        const payload = this._payload as CompletionRequest
        if (!payload?.content) return ''
        if (typeof payload.content === 'string') return payload.content
        const textPart = payload.content.find((c) => c.type === 'text')
        return textPart?.text || ''
      }
      case MessageType.CompletionResult:
        return (this._payload as CompletionResult)?.choices?.[0]?.message?.content ?? ''
      case MessageType.ToolResult:
        return (this._payload as ToolResult)?.content ?? ''
      default:
        return this.error?.message || ''
    }
  }

  public set content(content: string) {
    switch (this.originalType) {
      case MessageType.CompletionRequest: {
        const payload = this._payload as CompletionRequest
        if (typeof payload.content === 'string' || payload.content === null) {
          this._payload = { ...payload, content } as CompletionRequest
        } else if (Array.isArray(payload.content)) {
          const updated = JSON.parse(JSON.stringify(payload.content))
          if (updated.length > 0 && updated[0].type === 'text') {
            updated[0].text = content
          } else {
            updated.unshift({ type: 'text', text: content })
          }
          this._payload = { ...payload, content: updated } as CompletionRequest
        }
        break
      }
      case MessageType.ToolResult:
        this._payload = { ...this._payload, content } as ToolResult
        break
      case MessageType.CompletionResult: {
        const result = this._payload as CompletionResult
        const choices = result?.choices?.map((c) => ({ ...c })) ?? []
        if (choices[0]) {
          choices[0].message = { ...choices[0].message, content }
        }
        this._payload = { ...result, choices } as CompletionResult
        break
      }
    }
  }

  // Attachments (non-text content parts)

  public get attachments(): MessageContent[] {
    if (this.originalType !== MessageType.CompletionRequest) return []
    const payload = this._payload as CompletionRequest
    if (!Array.isArray(payload?.content)) return []
    return payload.content.filter((c) => c.type !== 'text')
  }

  public set attachments(parts: MessageContent[]) {
    if (this.originalType !== MessageType.CompletionRequest) return
    const payload = this._payload as CompletionRequest
    const text = this.content
    if (parts.length === 0) {
      this._payload = { ...payload, content: text } as CompletionRequest
    } else {
      this._payload = {
        ...payload,
        content: [{ type: 'text', text } as MessageContent, ...parts],
      } as CompletionRequest
    }
  }

  // Tool calls

  public get toolCalls(): ToolCall[] | undefined {
    if (this.originalType === MessageType.CompletionResult) {
      const calls = (this._payload as CompletionResult)?.choices
        ?.map((c) => c.message.tool_calls ?? [])
        .flat()
      return calls && calls.length > 0 ? calls : undefined
    }
    if (this.originalType === MessageType.CompletionRequest) {
      const calls = (this._payload as CompletionRequest)?.tool_calls
      return calls && calls.length > 0 ? calls : undefined
    }
    return undefined
  }

  public get toolCallId(): string | undefined {
    if (this.originalType === MessageType.ToolResult || this.currentType === MessageType.ToolResult) {
      return (this._payload as ToolResult)?.tool_call_id
    }
    if (this.originalType === MessageType.CompletionRequest) {
      return (this._payload as CompletionRequest)?.tool_call_id
    }
    return undefined
  }

  public set toolCallId(id: string) {
    if (this.originalType === MessageType.ToolResult) {
      (this._payload as ToolResult).tool_call_id = id
    } else if (this.originalType === MessageType.CompletionRequest) {
      (this._payload as CompletionRequest).tool_call_id = id
    }
  }

  public get isToolCallError(): boolean | undefined {
    if (
      this.originalType === MessageType.ToolResult ||
      this.currentType === MessageType.ToolResult
    ) {
      return !!(this._payload as ToolResult)?.isError
    }
    return undefined
  }

  public setToolCallError(isError: boolean | undefined): void {
    if (
      this.originalType === MessageType.ToolResult ||
      this.currentType === MessageType.ToolResult
    ) {
      this._payload = { ...this._payload, isError } as ToolResult
    }
  }

  public get finishReasons(): string | undefined {
    if (this.originalType === MessageType.CompletionResult) {
      return (this._payload as CompletionResult)?.choices?.find((c) => c.finish_reason)
        ?.finish_reason
    }
    return undefined
  }

  // Batch helpers

  static serializeAll(messages: Message[]): SerializedMessage[] {
    return messages.map((m) => m.serialized)
  }

  static deserializeAll(data: SerializedMessage[]): Message[] {
    return data.map((d) => Message.deserialize(d))
  }

  /**
   * Convert to OpenAI-compatible API format for chat completions.
   * Excludes error messages.
   */
  static toAPIMessages(messages: Message[]): APIMessage[] {
    return messages
      .filter((m) => m.type !== MessageType.CompletionError)
      .map((m): APIMessage => {
        switch (m.originalType) {
          case MessageType.CompletionRequest: {
            const p = m._payload as CompletionRequest
            const msg: APIMessage = { role: p.role, content: p.content }
            if (p.tool_calls && p.tool_calls.length > 0) msg.tool_calls = p.tool_calls
            if (p.tool_call_id) msg.tool_call_id = p.tool_call_id
            return msg
          }
          case MessageType.CompletionResult: {
            const choice = (m._payload as CompletionResult)?.choices?.[0]?.message
            const msg: APIMessage = {
              role: choice?.role ?? MessageRole.ASSISTANT,
              content: choice?.content ?? '',
            }
            if (choice?.tool_calls && choice.tool_calls.length > 0) {
              msg.tool_calls = choice.tool_calls
            }
            return msg
          }
          case MessageType.ToolResult: {
            const p = m._payload as ToolResult
            return {
              role: MessageRole.TOOL,
              content: p.content,
              tool_call_id: p.tool_call_id,
            }
          }
          default:
            return { role: MessageRole.ASSISTANT, content: m.content }
        }
      })
  }

  /**
   * Backward-compatible deserialization for old { role, content } format.
   * Detects whether data is the new serialized format or old flat format.
   */
  static fromLegacy(
    data: SerializedMessage | { role: string; content: string | null; tool_calls?: ToolCall[]; tool_call_id?: string },
    index: number
  ): Message {
    // New format has originalType
    if ('originalType' in data && data.originalType) {
      return Message.deserialize(data as SerializedMessage)
    }

    // Legacy { role, content } format
    const legacy = data as { role: string; content: string | null; tool_calls?: ToolCall[]; tool_call_id?: string }

    if (legacy.tool_calls && legacy.tool_calls.length > 0) {
      return new Message(uuidv4(), index, MessageType.CompletionResult, {
        id: uuidv4(),
        choices: [
          {
            index: 0,
            message: {
              role: MessageRole.ASSISTANT,
              content: (legacy.content as string) ?? '',
              tool_calls: legacy.tool_calls,
            },
          },
        ],
      })
    }

    if (legacy.role === 'tool' && legacy.tool_call_id) {
      return new Message(uuidv4(), index, MessageType.ToolResult, {
        role: MessageRole.TOOL,
        content: (legacy.content as string) ?? '',
        tool_call_id: legacy.tool_call_id,
      })
    }

    const role = (legacy.role as MessageRole) ?? MessageRole.USER
    if (role === MessageRole.ASSISTANT) {
      return new Message(uuidv4(), index, MessageType.CompletionResult, {
        id: uuidv4(),
        choices: [{ index: 0, message: { role: MessageRole.ASSISTANT, content: (legacy.content as string) ?? '' } }],
      })
    }

    return new Message(uuidv4(), index, MessageType.CompletionRequest, {
      role,
      content: legacy.content,
    } as CompletionRequest)
  }

  static fromLegacyAll(data: any[]): Message[] {
    return data.map((d, i) => Message.fromLegacy(d, i))
  }
}
