'use client'

import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { AzureKeyConfig } from '@/lib/types/config'

interface AzureConfigProps {
  keyIndex: number
  keyConfig: AzureKeyConfig
  onUpdate: (index: number, field: keyof AzureKeyConfig, value: string | Record<string, string>) => void
}

export function AzureConfig({ keyIndex, keyConfig, onUpdate }: AzureConfigProps) {
  return (
    <div className="space-y-4">
      <div>
        <label className="mb-2 block text-sm font-medium">Endpoint (Required)</label>
        <Input
          placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT"
          value={keyConfig?.endpoint || ''}
          onChange={(e) => onUpdate(keyIndex, 'endpoint', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">API Version (Optional)</label>
        <Input
          placeholder="2024-02-01 or env.AZURE_API_VERSION"
          value={keyConfig?.api_version || ''}
          onChange={(e) => onUpdate(keyIndex, 'api_version', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Deployments (Required)</label>
        <div className="text-muted-foreground mb-2 text-xs">
          JSON object mapping model names to deployment names
        </div>
        <Textarea
          placeholder='{"gpt-4": "my-gpt4-deployment", "gpt-3.5-turbo": "my-gpt35-deployment"}'
          value={
            typeof keyConfig?.deployments === 'string'
              ? keyConfig.deployments
              : JSON.stringify(keyConfig?.deployments || {}, null, 2)
          }
          onChange={(e) => {
            // Store as string during editing to allow intermediate invalid states
            onUpdate(keyIndex, 'deployments', e.target.value)
          }}
          onBlur={(e) => {
            // Try to parse as JSON on blur, but keep as string if invalid
            const value = e.target.value.trim()
            if (value) {
              try {
                const parsed = JSON.parse(value)
                if (typeof parsed === 'object' && parsed !== null) {
                  onUpdate(keyIndex, 'deployments', parsed)
                }
              } catch {
                // Keep as string for validation on submit
              }
            }
          }}
          rows={3}
          className="max-w-full font-mono text-sm wrap-anywhere"
        />
      </div>
    </div>
  )
}
