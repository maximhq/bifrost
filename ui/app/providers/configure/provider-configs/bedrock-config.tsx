'use client'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { BedrockKeyConfig } from '@/lib/types/config'
import { Info } from 'lucide-react'

interface BedrockConfigProps {
  keyIndex: number
  keyConfig: BedrockKeyConfig
  onUpdate: (index: number, field: keyof BedrockKeyConfig, value: string | Record<string, string>) => void
  showIAMAlert?: boolean
}

export function BedrockConfig({ keyIndex, keyConfig, onUpdate, showIAMAlert = false }: BedrockConfigProps) {
  return (
    <div className="space-y-4 pt-2">
      {showIAMAlert && (
        <Alert variant="default">
          <Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
          <AlertTitle>IAM Role Authentication</AlertTitle>
          <AlertDescription>
            Leave both Access Key and Secret Key empty to use IAM roles attached to your environment (EC2, Lambda, ECS, EKS).
            This is the recommended approach for production deployments.
          </AlertDescription>
        </Alert>
      )}

      <div>
        <label className="mb-2 block text-sm font-medium">Access Key</label>
        <Input
          placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID"
          value={keyConfig?.access_key || ''}
          onChange={(e) => onUpdate(keyIndex, 'access_key', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Secret Key</label>
        <Input
          placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY"
          value={keyConfig?.secret_key || ''}
          onChange={(e) => onUpdate(keyIndex, 'secret_key', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Session Token (Optional)</label>
        <Input
          placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN"
          value={keyConfig?.session_token || ''}
          onChange={(e) => onUpdate(keyIndex, 'session_token', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Region (Required)</label>
        <Input
          placeholder="us-east-1 or env.AWS_REGION"
          value={keyConfig?.region || ''}
          onChange={(e) => onUpdate(keyIndex, 'region', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Deployments (Optional)</label>
        <div className="text-muted-foreground mb-2 text-xs">
          JSON object mapping model names to inference profile names
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
