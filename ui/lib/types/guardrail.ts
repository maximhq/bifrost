export interface GuardrailProvider {
  id: string
  name: string
  type: string
  enabled: boolean
  config?: Record<string, any>
  createdAt?: string
  updatedAt?: string
}

