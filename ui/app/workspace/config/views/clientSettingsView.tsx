'use client'

import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { getErrorMessage, useGetCoreConfigQuery, useGetDroppedRequestsQuery, useUpdateCoreConfigMutation } from '@/lib/store'
import { CoreConfig } from '@/lib/types/config'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'

const defaultConfig: CoreConfig = {
  drop_excess_requests: false,
  initial_pool_size: 1000,
  prometheus_labels: [],
  enable_logging: true,
  enable_governance: true,
  enforce_governance_header: false,
  allow_direct_keys: false,
  allowed_origins: [],
  max_request_body_size_mb: 100,
  enable_litellm_fallbacks: false
}

export default function ClientSettingsView () {
  const [droppedRequests, setDroppedRequests] = useState<number>(0)
  const { data: droppedRequestsData } = useGetDroppedRequestsQuery()
  const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true })
  const config = bifrostConfig?.client_config
  const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation()
  const [localConfig, setLocalConfig] = useState<CoreConfig>(defaultConfig)

  useEffect(() => {
    if (droppedRequestsData) {
      setDroppedRequests(droppedRequestsData.dropped_requests)
    }
  }, [droppedRequestsData])

  useEffect(() => {
    if (config) {
      setLocalConfig(config)
    }
  }, [config])

  const hasChanges = useMemo(() => {
    if (!config) return false
    return (
      localConfig.drop_excess_requests !== config.drop_excess_requests ||
      localConfig.enforce_governance_header !== config.enforce_governance_header ||
      localConfig.allow_direct_keys !== config.allow_direct_keys ||
      localConfig.enable_litellm_fallbacks !== config.enable_litellm_fallbacks
    )
  }, [config, localConfig])

  const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean | number | string[]) => {
    setLocalConfig((prev) => ({ ...prev, [field]: value }))
  }, [])

  const handleSave = useCallback(
    async () => {
      try {
        await updateCoreConfig({ ...bifrostConfig!, client_config: localConfig }).unwrap()
        toast.success('Client settings updated successfully.')
      } catch (error) {
        toast.error(getErrorMessage(error))
      }
    },
    [bifrostConfig, localConfig, updateCoreConfig]
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Client Settings</h2>
          <p className="text-muted-foreground text-sm">Configure client behavior and request handling.</p>
        </div>
        <Button onClick={handleSave} disabled={!hasChanges || isLoading}>
          {isLoading ? 'Saving...' : 'Save Changes'}
        </Button>
      </div>

      <div className="space-y-4">
        {/* Drop Excess Requests */}
        <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="drop-excess-requests" className="text-sm font-medium">
              Drop Excess Requests
            </label>
            <p className="text-muted-foreground text-sm">
              If enabled, Bifrost will drop requests that exceed pool capacity.{' '}
              {localConfig.drop_excess_requests && droppedRequests > 0 ? (
                <span>
                  Have dropped <b>{droppedRequests} requests</b> since last restart.
                </span>
              ) : (
                <></>
              )}
            </p>
          </div>
          <Switch
            id="drop-excess-requests"
            size="md"
            checked={localConfig.drop_excess_requests}
            onCheckedChange={(checked) => handleConfigChange('drop_excess_requests', checked)}
          />
        </div>

        {/* Enforce Virtual Keys */}
        {localConfig.enable_governance && (
          <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
            <div className="space-y-0.5">
              <label htmlFor="enforce-governance" className="text-sm font-medium">
                Enforce Virtual Keys
              </label>
              <p className="text-muted-foreground text-sm">
                Enforce the use of a virtual key for all requests. If enabled, requests without the <b>x-bf-vk</b> header will be
                rejected.
              </p>
            </div>
            <Switch
              id="enforce-governance"
              size="md"
              checked={localConfig.enforce_governance_header}
              onCheckedChange={(checked) => handleConfigChange('enforce_governance_header', checked)}
            />
          </div>
        )}

        {/* Allow Direct API Keys */}
        <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="allow-direct-keys" className="text-sm font-medium">
              Allow Direct API Keys
            </label>
            <p className="text-muted-foreground text-sm">
              Allow API keys to be passed directly in request headers (<b>Authorization</b> or <b>x-api-key</b>). Bifrost will directly
              use the key.
            </p>
          </div>
          <Switch
            id="allow-direct-keys"
            size="md"
            checked={localConfig.allow_direct_keys}
            onCheckedChange={(checked) => handleConfigChange('allow_direct_keys', checked)}
          />
        </div>

        {/* Enable LiteLLM Fallbacks */}
        <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="enable-litellm-fallbacks" className="text-sm font-medium">
              Enable LiteLLM Fallbacks
            </label>
            <p className="text-muted-foreground text-sm">
              Enable litellm-specific fallbacks for text completion for Groq.
            </p>
          </div>
          <Switch
            id="enable-litellm-fallbacks"
            size="md"
            checked={localConfig.enable_litellm_fallbacks}
            onCheckedChange={(checked) => handleConfigChange('enable_litellm_fallbacks', checked)}
          />
        </div>
      </div>
    </div>
  )
}

