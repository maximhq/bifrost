// Prompt Repository types for frontend
import type { SerializedMessage } from '@/lib/message'

export interface PromptUser {
  id: string
  name: string
  email: string
}

export type { MessageContent, MessageFile, MessageImageURL, MessageInputAudio, SerializedMessage } from '@/lib/message'

export interface Folder {
  id: string
  name: string
  description?: string
  created_by_id?: number
  created_by?: PromptUser
  created_at: string
  updated_at: string
  prompts_count?: number
}

export interface Prompt {
  id: string
  name: string
  folder_id?: string
  folder?: Folder
  created_by_id?: number
  created_by?: PromptUser
  created_at: string
  updated_at: string
  latest_version?: PromptVersion
}

export interface ModelParams {
  temperature?: number
  max_tokens?: number
  top_p?: number
  frequency_penalty?: number
  presence_penalty?: number
  stop?: string[]
  [key: string]: any
}

export interface PromptVersion {
  id: number
  prompt_id: string
  version_number: number
  commit_message: string
  messages: PromptVersionMessage[]
  model_params: ModelParams
  provider: string
  model: string
  variables?: Record<string, string>
  is_latest: boolean
  created_by_id?: number
  created_by?: PromptUser
  created_at: string // No updated_at - versions are immutable
}

export interface PromptVersionMessage {
  id: number
  prompt_id: string
  version_id: number
  order_index: number
  message: PromptMessage
}

export interface PromptSession {
  id: number
  prompt_id: string
  prompt?: Prompt
  version_id?: number
  version?: PromptVersion
  name: string
  messages: PromptSessionMessage[]
  model_params: ModelParams
  provider: string
  model: string
  variables?: Record<string, string>
  created_by_id?: number
  created_by?: PromptUser
  created_at: string
  updated_at: string
}

export interface PromptSessionMessage {
  id: number
  prompt_id: string
  session_id: number
  order_index: number
  message: PromptMessage
}

// ============================================================================
// Message Types (OpenAI-compatible format)
// ============================================================================

export type PromptMessage = SerializedMessage

// ============================================================================
// API Request/Response Types - Folders
// ============================================================================

export interface GetFoldersResponse {
  folders: Folder[]
}

export interface GetFolderResponse {
  folder: Folder
}

export interface CreateFolderRequest {
  name: string
  description?: string
}

export interface CreateFolderResponse {
  folder: Folder
}

export interface UpdateFolderRequest {
  name?: string
  description?: string
}

export interface UpdateFolderResponse {
  folder: Folder
}

export interface DeleteFolderResponse {
  message: string
}

// ============================================================================
// API Request/Response Types - Prompts
// ============================================================================

export interface GetPromptsResponse {
  prompts: Prompt[]
}

export interface GetPromptResponse {
  prompt: Prompt
}

export interface CreatePromptRequest {
  name: string
  folder_id?: string
}

export interface CreatePromptResponse {
  prompt: Prompt
}

export interface UpdatePromptRequest {
  name?: string
  folder_id?: string | null
}

export interface UpdatePromptResponse {
  prompt: Prompt
}

export interface DeletePromptResponse {
  message: string
}

// ============================================================================
// API Request/Response Types - Versions
// ============================================================================

export interface GetVersionsResponse {
  versions: PromptVersion[]
}

export interface GetVersionResponse {
  version: PromptVersion
}

export interface CreateVersionRequest {
  commit_message: string
  messages: PromptMessage[]
  model_params: ModelParams
  provider: string
  model: string
}

export interface CreateVersionResponse {
  version: PromptVersion
}

export interface DeleteVersionResponse {
  message: string
}

// ============================================================================
// API Request/Response Types - Sessions
// ============================================================================

export interface GetSessionsResponse {
  sessions: PromptSession[]
}

export interface GetSessionResponse {
  session: PromptSession
}

export interface CreateSessionRequest {
  name?: string
  version_id?: number
  messages?: PromptMessage[]
  model_params: ModelParams
  provider: string
  model: string
  variables?: Record<string, string>
}

export interface CreateSessionResponse {
  session: PromptSession
}

export interface UpdateSessionRequest {
  name?: string
  messages: PromptMessage[]
  model_params: ModelParams
  provider: string
  model: string
  variables?: Record<string, string>
}

export interface UpdateSessionResponse {
  session: PromptSession
}

export interface RenameSessionRequest {
  name: string
}

export interface RenameSessionResponse {
  session: PromptSession
}

export interface DeleteSessionResponse {
  message: string
}

export interface CommitSessionRequest {
  commit_message: string
}

export interface CommitSessionResponse {
  version: PromptVersion
}
