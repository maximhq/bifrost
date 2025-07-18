import React from 'react'
import { CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Info, PickaxeIcon } from 'lucide-react'
import { MetaConfig } from '@/lib/types/config'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

type FieldType = 'text' | 'textarea'

interface MetaField {
  name: keyof MetaConfig
  label: string
  type: FieldType
  placeholder?: string
  isJson?: boolean
}

const providerMetaFields: Record<string, { title: string; fields: MetaField[] }> = {
  bedrock: {
    title: 'AWS Bedrock Meta Config',
    fields: [
      {
        name: 'region',
        label: 'Region',
        type: 'text',
        placeholder: 'us-east-1 or env.AWS_REGION',
      },
      {
        name: 'secret_access_key',
        label: 'Secret Access Key (Optional)',
        type: 'text',
        placeholder: 'Your AWS secret access key or env.AWS_SECRET_ACCESS_KEY',
      },
      {
        name: 'session_token',
        label: 'Session Token (Optional)',
        type: 'text',
        placeholder: 'Your AWS session token or env.AWS_SESSION_TOKEN',
      },
      {
        name: 'arn',
        label: 'ARN (Optional)',
        type: 'text',
        placeholder: 'AWS ARN or env.AWS_ARN',
      },
      {
        name: 'inference_profiles',
        label: 'Inference Profiles (JSON format, Optional)',
        type: 'textarea',
        placeholder: '{ "model-id": "profile-name" }',
        isJson: true,
      },
    ],
  },
}

interface MetaConfigRendererProps {
  provider: string
  metaConfig: MetaConfig
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  onMetaConfigChange: (key: keyof MetaConfig, value: any) => void
}

const MetaConfigRenderer: React.FC<MetaConfigRendererProps> = ({ provider, metaConfig, onMetaConfigChange }) => {
  const config = providerMetaFields[provider]

  if (!config) {
    return null
  }

  const renderField = (field: MetaField) => {
    const value = metaConfig[field.name]

    if (field.type === 'textarea') {
      return (
        <Textarea
          placeholder={field.placeholder}
          value={field.isJson ? (typeof value === 'string' ? value : JSON.stringify(value, null, 2)) : (value as string) || ''}
          onChange={(e) => {
            onMetaConfigChange(field.name, e.target.value)
          }}
          onBlur={(e) => {
            if (field.isJson) {
              try {
                const parsed = JSON.parse(e.target.value)
                onMetaConfigChange(field.name, parsed)
              } catch {
                // Ignore parsing errors, keep it as string
              }
            }
          }}
          rows={4}
          className="wrap-anywhere max-w-full font-mono text-sm"
        />
      )
    }

    return (
      <Input
        placeholder={field.placeholder}
        value={(value as string) || ''}
        onChange={(e) => onMetaConfigChange(field.name, e.target.value)}
      />
    )
  }

  return (
    <div className="">
      <CardHeader className="mb-2 px-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <PickaxeIcon className="h-4 w-4" />
          {config.title}
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <span>
                  <Info className="text-muted-foreground ml-1 h-3 w-3" />
                </span>
              </TooltipTrigger>
              <TooltipContent className="max-w-fit">
                <p>
                  Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read the value from an
                  environment variable.
                </p>
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 px-0">
        {config.fields.map((field) => (
          <div key={field.name}>
            <label className="block text-sm font-medium">{field.label}</label>
            {renderField(field)}
          </div>
        ))}
      </CardContent>
    </div>
  )
}

export default MetaConfigRenderer
