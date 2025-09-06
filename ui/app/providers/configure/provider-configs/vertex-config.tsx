'use client'

import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { VertexKeyConfig } from '@/lib/types/config'
import { isRedacted } from '@/lib/utils/validation'
import { Info } from 'lucide-react'

interface VertexConfigProps {
  keyIndex: number
  keyConfig: VertexKeyConfig
  onUpdate: (index: number, field: keyof VertexKeyConfig, value: string) => void
}

export function VertexConfig({ keyIndex, keyConfig, onUpdate }: VertexConfigProps) {
  return (
    <div className="space-y-4 pt-2">
      <div>
        <label className="mb-2 block text-sm font-medium">Project ID (Required)</label>
        <Input
          placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID"
          value={keyConfig?.project_id || ''}
          onChange={(e) => onUpdate(keyIndex, 'project_id', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Region (Required)</label>
        <Input
          placeholder="us-central1 or env.VERTEX_REGION"
          value={keyConfig?.region || ''}
          onChange={(e) => onUpdate(keyIndex, 'region', e.target.value)}
        />
      </div>

      <div>
        <label className="mb-2 block text-sm font-medium">Auth Credentials (Required)</label>
        <div className="text-muted-foreground mb-2 text-xs">Service account JSON object or env.VAR_NAME</div>
        <Textarea
          placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
          value={keyConfig?.auth_credentials || ''}
          onChange={(e) => {
            // Always store as string - backend expects string type
            onUpdate(keyIndex, 'auth_credentials', e.target.value)
          }}
          rows={4}
          className="max-w-full font-mono text-sm wrap-anywhere"
        />
        {isRedacted(keyConfig?.auth_credentials || '') && (
          <div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
            <Info className="h-3 w-3" />
            <span>Credentials are stored securely. Edit to update.</span>
          </div>
        )}
      </div>
    </div>
  )
}
