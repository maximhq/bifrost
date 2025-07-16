export const PROVIDERS = ['openai', 'anthropic', 'azure', 'bedrock', 'cohere', 'vertex', 'mistral', 'ollama', 'groq', 'sgl'] as const

export const STATUSES = ['success', 'error', 'cancelled'] as const

export const REQUEST_TYPES = ['chat.completion', 'text.completion', 'embedding', 'chat.completion.chunk'] as const

export const PROVIDER_LABELS = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  azure: 'Azure OpenAI',
  bedrock: 'AWS Bedrock',
  cohere: 'Cohere',
  vertex: 'Vertex AI',
  mistral: 'Mistral AI',
  ollama: 'Ollama',
  groq: 'Groq',
  sgl: 'SGLang',
} as const

export const STATUS_COLORS = {
  success: 'bg-green-100 text-green-800',
  error: 'bg-red-100 text-red-800',
  cancelled: 'bg-gray-100 text-gray-800',
} as const

export const REQUEST_TYPE_LABELS = {
  'chat.completion': 'Chat',
  'text.completion': 'Text',
  embedding: 'Embedding',
  'chat.completion.chunk': 'Stream',
} as const

export const REQUEST_TYPE_COLORS = {
  'chat.completion': 'bg-blue-100 text-blue-800',
  'text.completion': 'bg-green-100 text-green-800',
  embedding: 'bg-red-100 text-red-800',
  'chat.completion.chunk': 'bg-yellow-100 text-yellow-800',
} as const

export type Provider = (typeof PROVIDERS)[number]
export type Status = (typeof STATUSES)[number]
