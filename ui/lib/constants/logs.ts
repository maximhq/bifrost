export const PROVIDERS = ['openai', 'anthropic', 'azure', 'bedrock', 'cohere', 'vertex', 'mistral', 'ollama', 'groq', 'sgl'] as const

export const STATUSES = ['success', 'error', 'processing', 'cancelled'] as const

export const REQUEST_TYPES = [
  'chat.completion',
  'text.completion',
  'embedding',
  'list',
  'audio.speech',
  'audio.transcription',
  'chat.completion.chunk',
  'audio.speech.chunk',
  'audio.transcription.chunk',
] as const

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
  processing: 'bg-blue-100 text-blue-800',
  cancelled: 'bg-gray-100 text-gray-800',
} as const

export const REQUEST_TYPE_LABELS = {
  'chat.completion': 'Chat',
  'text.completion': 'Text',
  embedding: 'Embedding',
  list: 'List',
  'audio.speech': 'Speech',
  'audio.transcription': 'Transcription',
  'chat.completion.chunk': 'Chat Stream',
  'audio.speech.chunk': 'Speech Stream',
  'audio.transcription.chunk': 'Transcription Stream',
} as const

export const REQUEST_TYPE_COLORS = {
  'chat.completion': 'bg-blue-100 text-blue-800',
  'text.completion': 'bg-green-100 text-green-800',
  embedding: 'bg-red-100 text-red-800',
  list: 'bg-red-100 text-red-800',
  'audio.speech': 'bg-purple-100 text-purple-800',
  'audio.transcription': 'bg-orange-100 text-orange-800',
  'chat.completion.chunk': 'bg-yellow-100 text-yellow-800',
  'audio.speech.chunk': 'bg-pink-100 text-pink-800',
  'audio.transcription.chunk': 'bg-lime-100 text-lime-800',
} as const

export type Provider = (typeof PROVIDERS)[number]
export type Status = (typeof STATUSES)[number]
