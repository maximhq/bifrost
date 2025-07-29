'use client'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { TagInput } from '@/components/ui/tag-input'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { apiService } from '@/lib/api'
import { DEFAULT_NETWORK_CONFIG, DEFAULT_PERFORMANCE_CONFIG } from '@/lib/constants/config'
import { ProviderIconType, renderProviderIcon } from '@/lib/constants/icons'
import { PROVIDER_LABELS, PROVIDERS as Providers } from '@/lib/constants/logs'
import {
  AddProviderRequest,
  AzureKeyConfig,
  ConcurrencyAndBufferSize,
  Key as KeyType,
  MetaConfig,
  ModelProvider,
  NetworkConfig,
  ProviderResponse,
  ProxyConfig,
  ProxyType,
  UpdateProviderRequest,
  VertexKeyConfig,
} from '@/lib/types/config'
import { cn } from '@/lib/utils'
import { Validator } from '@/lib/utils/validation'
import { isRedacted, isValidVertexAuthCredentials, isValidAzureDeployments } from '@/lib/utils/validation'
import isEqual from 'lodash.isequal'
import { AlertTriangle, Globe, Info, Plus, Save, X, Zap } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'
import { Alert, AlertDescription } from '../ui/alert'
import MetaConfigRenderer from './meta-config-renderer'
import { Textarea } from '../ui/textarea'

interface ProviderFormProps {
  provider?: ProviderResponse | null
  onSave: () => void
  onCancel: () => void
  existingProviders: string[]
}

// A helper function to create a clean initial state
const createInitialState = (provider?: ProviderResponse | null, defaultProvider?: string): Omit<ProviderFormData, 'isDirty'> => {
  const isNewProvider = !provider
  const providerName = provider?.name || defaultProvider || ''
  const keysRequired = !['ollama', 'sgl'].includes(providerName) // Vertex needs keys for config

  // Create default key based on provider type
  const createDefaultKey = (): KeyType => {
    const baseKey: KeyType = { id: '', value: '', models: [], weight: 1.0 }

    if (providerName === 'azure') {
      baseKey.azure_key_config = {
        endpoint: '',
        deployments: {},
        api_version: '2024-02-01',
      }
    } else if (providerName === 'vertex') {
      baseKey.vertex_key_config = {
        project_id: '',
        region: '',
        auth_credentials: '',
      }
    }

    return baseKey
  }

  return {
    selectedProvider: providerName,
    keys: isNewProvider && keysRequired ? [createDefaultKey()] : !isNewProvider && keysRequired && provider?.keys ? provider.keys : [],
    networkConfig: provider?.network_config || DEFAULT_NETWORK_CONFIG,
    performanceConfig: provider?.concurrency_and_buffer_size || DEFAULT_PERFORMANCE_CONFIG,
    metaConfig: provider?.meta_config || {
      region: '',
      secret_access_key: '',
    },
    proxyConfig: provider?.proxy_config || {
      type: 'none',
      url: '',
      username: '',
      password: '',
    },
    sendBackRawResponse: provider?.send_back_raw_response || false,
  }
}

interface ProviderFormData {
  selectedProvider: string
  keys: KeyType[]
  networkConfig: NetworkConfig
  performanceConfig: ConcurrencyAndBufferSize
  metaConfig: MetaConfig
  proxyConfig: ProxyConfig
  sendBackRawResponse: boolean
  isDirty: boolean
}

export default function ProviderForm({ provider, onSave, onCancel, existingProviders }: ProviderFormProps) {
  // Find the first available provider if adding a new provider
  const firstAvailableProvider = !provider ? Providers.find((p) => !existingProviders.includes(p)) || '' : undefined
  const [initialState] = useState<Omit<ProviderFormData, 'isDirty'>>(createInitialState(provider, firstAvailableProvider))
  const [formData, setFormData] = useState<ProviderFormData>({
    ...initialState,
    isDirty: false,
  })
  const [isLoading, setIsLoading] = useState(false)

  const { selectedProvider, keys, networkConfig, performanceConfig, metaConfig, proxyConfig, sendBackRawResponse, isDirty } = formData

  const baseURLRequired = selectedProvider === 'ollama' || selectedProvider === 'sgl'
  const keysRequired = !['ollama', 'sgl'].includes(selectedProvider) // Vertex needs keys for config
  const keysValid = !keysRequired || keys.every((k) => selectedProvider === 'vertex' || k.value.trim() !== '') // Vertex can have empty API key
  const keysPresent = !keysRequired || keys.length > 0

  const performanceValid =
    performanceConfig.concurrency > 0 && performanceConfig.buffer_size > 0 && performanceConfig.concurrency < performanceConfig.buffer_size

  // Track if performance settings have changed
  const performanceChanged =
    performanceConfig.concurrency !== initialState.performanceConfig.concurrency ||
    performanceConfig.buffer_size !== initialState.performanceConfig.buffer_size

  /* Key-level configuration validation for Azure and Vertex */
  const getKeyValidation = () => {
    let valid = true
    let message = ''

    for (const key of keys) {
      if (selectedProvider === 'azure' && key.azure_key_config) {
        const endpointValid = !!key.azure_key_config.endpoint && key.azure_key_config.endpoint.trim() !== ''

        // Validate deployments using utility function
        const deploymentsValid = isValidAzureDeployments(key.azure_key_config.deployments)

        if (!endpointValid || !deploymentsValid) {
          valid = false
          message = 'Endpoint and valid Deployments (JSON object) are required for Azure keys'
          break
        }
      } else if (selectedProvider === 'vertex' && key.vertex_key_config) {
        const projectValid = !!key.vertex_key_config.project_id && key.vertex_key_config.project_id.trim() !== ''
        const regionValid = !!key.vertex_key_config.region && key.vertex_key_config.region.trim() !== ''

        // Validate auth credentials using utility function
        const credsValid = isValidVertexAuthCredentials(key.vertex_key_config.auth_credentials)

        if (!projectValid || !credsValid || !regionValid) {
          valid = false
          message = 'Project ID, valid Auth Credentials (JSON object or env.VAR), and Region are required for Vertex AI keys'
          break
        }
      }
    }

    return { valid, message }
  }

  /* Meta configuration validation based on provider requirements (Bedrock only) */
  const getMetaValidation = () => {
    let valid = true
    let message = ''

    if (selectedProvider === 'bedrock') {
      const regionValid = !!metaConfig.region && (metaConfig.region as string).trim() !== ''
      valid = regionValid
      if (!valid) {
        message = 'Region is required for AWS Bedrock'
      }
    }

    return { valid, message }
  }

  const { valid: keyValid, message: keyErrorMessage } = getKeyValidation()
  const { valid: metaValid, message: metaErrorMessage } = getMetaValidation()

  useEffect(() => {
    const currentData = {
      selectedProvider,
      keys: keysRequired ? keys : [],
      networkConfig,
      performanceConfig,
      metaConfig,
      proxyConfig,
    }
    setFormData((prev) => ({
      ...prev,
      isDirty: !isEqual(initialState, currentData),
    }))
  }, [selectedProvider, keys, networkConfig, performanceConfig, metaConfig, proxyConfig, initialState, keysRequired])

  const updateField = <K extends keyof ProviderFormData>(field: K, value: ProviderFormData[K]) => {
    setFormData((prev) => ({ ...prev, [field]: value }))
  }

  const updateProxyField = <K extends keyof ProxyConfig>(field: K, value: ProxyConfig[K]) => {
    updateField('proxyConfig', { ...proxyConfig, [field]: value })
  }

  const availableProviders = provider ? Providers : Providers.filter((p) => !existingProviders.includes(p))

  const handleSubmit = async (e: React.FormEvent) => {
    if (!validator.isValid()) {
      toast.error(validator.getFirstError())
      return
    }

    e.preventDefault()
    setIsLoading(true)

    let error: string | null = null

    if (provider) {
      const data: UpdateProviderRequest = {
        keys: keysRequired
          ? keys.filter((k) =>
              selectedProvider === 'vertex'
                ? true // Include all Vertex keys (API key can be empty)
                : k.value.trim() !== '',
            )
          : [],
        network_config: networkConfig,
        concurrency_and_buffer_size: performanceConfig,
        meta_config: metaConfig,
        proxy_config: proxyConfig,
        send_back_raw_response: sendBackRawResponse,
      }
      ;[, error] = await apiService.updateProvider(provider.name, data)
    } else {
      const data: AddProviderRequest = {
        provider: selectedProvider as ModelProvider,
        keys: keysRequired
          ? keys.filter((k) =>
              selectedProvider === 'vertex'
                ? true // Include all Vertex keys (API key can be empty)
                : k.value.trim() !== '',
            )
          : [],
        network_config: networkConfig,
        concurrency_and_buffer_size: performanceConfig,
        meta_config: metaConfig,
        proxy_config: proxyConfig,
      }
      ;[, error] = await apiService.createProvider(data)
    }

    setIsLoading(false)

    if (error) {
      toast.error(error)
    } else {
      toast.success(`Provider ${provider ? 'updated' : 'added'} successfully`)
      onSave()
    }
  }

  const validator = new Validator([
    // Provider selection
    Validator.required(selectedProvider, 'Please select a provider'),

    // Check if anything is dirty
    Validator.custom(isDirty, 'No changes to save'),

    // Base URL validation
    ...(baseURLRequired
      ? [
          Validator.required(networkConfig.base_url, 'Base URL is required for Ollama provider'),
          Validator.pattern(networkConfig.base_url || '', /^https?:\/\/.+/, 'Base URL must start with http:// or https://'),
        ]
      : []),

    // API Keys validation
    ...(keysRequired
      ? [
          Validator.minValue(keys.length, 1, 'At least one API key is required'),
          Validator.custom(
            keys.every((k) => selectedProvider === 'vertex' || k.value.trim() !== ''),
            'API key value cannot be empty',
          ),
          Validator.custom(
            keys.every((k) => k.weight >= 0 && k.weight <= 1),
            'Key weights must be between 0 and 1',
          ),
        ]
      : []),

    // Network config validation
    Validator.minValue(networkConfig.default_request_timeout_in_seconds, 1, 'Timeout must be greater than 0 seconds'),
    Validator.minValue(networkConfig.max_retries, 0, 'Max retries cannot be negative'),

    // Performance config validation
    Validator.minValue(performanceConfig.concurrency, 1, 'Concurrency must be greater than 0'),
    Validator.minValue(performanceConfig.buffer_size, 1, 'Buffer size must be greater than 0'),
    Validator.custom(performanceConfig.concurrency < performanceConfig.buffer_size, 'Buffer size must be greater than concurrency'),

    // Key-level config validation
    Validator.custom(keyValid, keyErrorMessage),

    // Meta config validation (Bedrock only)
    Validator.custom(metaValid, metaErrorMessage),

    // Meta config validation for Bedrock
    ...(selectedProvider === 'bedrock' ? [Validator.required(metaConfig.region, 'AWS region is required')] : []),
  ])

  const addKey = () => {
    const newKey: KeyType = { id: '', value: '', models: [], weight: 1.0 }

    if (selectedProvider === 'azure') {
      newKey.azure_key_config = {
        endpoint: '',
        deployments: {},
        api_version: '2024-02-01',
      }
    } else if (selectedProvider === 'vertex') {
      newKey.vertex_key_config = {
        project_id: '',
        region: '',
        auth_credentials: '',
      }
    }

    updateField('keys', [...keys, newKey])
  }

  const removeKey = (index: number) => {
    updateField(
      'keys',
      keys.filter((_, i) => i !== index),
    )
  }

  const updateKey = (index: number, field: keyof KeyType, value: string | number | string[]) => {
    const newKeys = [...keys]
    const keyToUpdate = { ...newKeys[index] }

    if (field === 'models' && Array.isArray(value)) {
      keyToUpdate.models = value
    } else if (field === 'value' && typeof value === 'string') {
      keyToUpdate.value = value
    } else if (field === 'weight' && typeof value === 'string') {
      keyToUpdate.weight = Number.parseFloat(value) || 1.0
    }

    newKeys[index] = keyToUpdate
    updateField('keys', newKeys)
  }

  const updateKeyAzureConfig = (index: number, field: keyof AzureKeyConfig, value: string | Record<string, string>) => {
    const newKeys = [...keys]
    const keyToUpdate = { ...newKeys[index] }

    if (!keyToUpdate.azure_key_config) {
      keyToUpdate.azure_key_config = {
        endpoint: '',
        deployments: {},
        api_version: '2024-02-01',
      }
    }

    keyToUpdate.azure_key_config = {
      ...keyToUpdate.azure_key_config,
      [field]: value,
    }

    newKeys[index] = keyToUpdate
    updateField('keys', newKeys)
  }

  const updateKeyVertexConfig = (index: number, field: keyof VertexKeyConfig, value: string) => {
    const newKeys = [...keys]
    const keyToUpdate = { ...newKeys[index] }

    if (!keyToUpdate.vertex_key_config) {
      keyToUpdate.vertex_key_config = {
        project_id: '',
        region: '',
        auth_credentials: '',
      }
    }

    keyToUpdate.vertex_key_config = {
      ...keyToUpdate.vertex_key_config,
      [field]: value,
    }

    newKeys[index] = keyToUpdate
    updateField('keys', newKeys)
  }

  const handleMetaConfigChange = (field: keyof MetaConfig, value: string | Record<string, string>) => {
    updateField('metaConfig', { ...metaConfig, [field]: value })
  }

  const tabs = useMemo(() => {
    const availableTabs = []

    // Only add API Keys tab if required for this provider
    if (keysRequired) {
      availableTabs.push({
        id: 'api-keys',
        label: 'API Keys',
      })
    }

    // Add Meta Config tab only for Bedrock
    if (selectedProvider === 'bedrock') {
      availableTabs.push({
        id: 'meta-config',
        label: 'Meta Config',
      })
    }

    // Network tab is always available
    availableTabs.push({
      id: 'network',
      label: 'Network',
    })

    // Performance tab is always available
    availableTabs.push({
      id: 'performance',
      label: 'Performance',
    })

    return availableTabs
  }, [keysRequired, selectedProvider])

  const [selectedTab, setSelectedTab] = useState(tabs[0]?.id || 'api-keys')

  useEffect(() => {
    if (!tabs.map((t) => t.id).includes(selectedTab)) {
      setSelectedTab(tabs[0]?.id || 'api-keys')
    }
  }, [tabs])

  return (
    <Dialog open={true} onOpenChange={onCancel}>
      <DialogContent className="custom-scrollbar max-h-[90vh] overflow-y-auto p-0 sm:max-w-4xl" showCloseButton={false}>
        <DialogHeader className="z-10 px-6 pt-6">
          <DialogTitle>
            {provider ? (
              <div className="flex items-center gap-2">
                {renderProviderIcon(provider.name as ProviderIconType, { size: 20 })}
                <span className="font-semibold">{PROVIDER_LABELS[provider.name]}</span>
              </div>
            ) : (
              <div className="flex items-center gap-2">Add Provider</div>
            )}
          </DialogTitle>
          <DialogDescription>Configure AI provider settings, API keys, and network options.</DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex flex-row gap-2 px-6 pt-6">
          {/* Provider Selection */}
          {!provider && (
            <TooltipProvider>
              <div className="flex w-[250px] flex-col gap-1 pb-10">
                {Providers.map((p) => (
                  <Tooltip key={p}>
                    <TooltipTrigger
                      className={cn(
                        'flex w-full items-center gap-2 rounded-lg border px-4 py-2 text-sm transition-all duration-200 ease-in-out',
                        selectedProvider === p
                          ? 'bg-secondary opacity-100 hover:opacity-100'
                          : availableProviders.includes(p)
                            ? 'hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border'
                            : 'cursor-not-allowed border-transparent opacity-30',
                      )}
                      onClick={(e) => {
                        e.preventDefault()
                        if (availableProviders.includes(p)) {
                          updateField('selectedProvider', p)
                        }
                      }}
                      asChild
                    >
                      <span>
                        {renderProviderIcon(p as ProviderIconType, { size: 'sm', className: 'w-5 h-5' })}
                        <div className="text-sm">{PROVIDER_LABELS[p as keyof typeof PROVIDER_LABELS]}</div>
                      </span>
                    </TooltipTrigger>
                    {!availableProviders.includes(p) && <TooltipContent>Provider is already configured</TooltipContent>}
                  </Tooltip>
                ))}
              </div>
            </TooltipProvider>
          )}

          <div className="flex h-full w-full flex-col justify-between px-2">
            <Tabs defaultValue={tabs[0]?.id} value={selectedTab} onValueChange={setSelectedTab} className="space-y-6">
              <TabsList style={{ gridTemplateColumns: `repeat(${tabs.length}, 1fr)` }} className={`mb-4 grid h-10 w-full`}>
                {tabs.map((tab) => (
                  <TabsTrigger key={tab.id} value={tab.id} className="flex items-center gap-2 transition-all duration-200 ease-in-out">
                    {tab.label}
                  </TabsTrigger>
                ))}
              </TabsList>

              {/* Animated Container for Tab Content */}
              <div className="relative overflow-hidden">
                <div
                  className="transition-all duration-300 ease-in-out"
                  style={{
                    maxHeight: '2000px',
                    opacity: 1,
                  }}
                >
                  {/* API Keys Tab */}
                  {keysRequired && selectedTab === 'api-keys' && (
                    <div className="animate-in fade-in-0 slide-in-from-right-2 space-y-4 duration-300">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2">
                          <h3 className="text-base font-medium">API Keys</h3>
                          <TooltipProvider>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span>
                                  <Info className="text-muted-foreground ml-1 h-3 w-3" />
                                </span>
                              </TooltipTrigger>
                              <TooltipContent className="max-w-fit">
                                <p>
                                  Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read
                                  the value from an environment variable.
                                </p>
                              </TooltipContent>
                            </Tooltip>
                          </TooltipProvider>
                        </div>
                        <Button type="button" variant="outline" size="sm" onClick={addKey}>
                          <Plus className="h-4 w-4" />
                          Add Key
                        </Button>
                      </div>
                      <div className="space-y-4">
                        {keys.map((key, index) => (
                          <div
                            key={index}
                            className="animate-in fade-in-0 slide-in-from-right-2 space-y-4 rounded-md border p-4 duration-300"
                            style={{ animationDelay: `${index * 50}ms` }}
                          >
                            <div className="flex gap-4">
                              {selectedProvider !== 'vertex' && (
                                <div className="flex-1">
                                  <div className="text-sm font-medium">API Key</div>
                                  <Input
                                    placeholder="API Key or env.MY_KEY"
                                    value={key.value}
                                    onChange={(e) => updateKey(index, 'value', e.target.value)}
                                    type="text"
                                    className={`flex-1 transition-all duration-200 ease-in-out ${keysRequired && selectedProvider !== 'vertex' && key.value.trim() === '' ? 'border-destructive' : ''}`}
                                  />
                                </div>
                              )}

                              <div>
                                <div className="flex items-center gap-4">
                                  <label className="text-sm font-medium">Weight</label>
                                  <TooltipProvider>
                                    <Tooltip>
                                      <TooltipTrigger asChild>
                                        <span>
                                          <Info className="text-muted-foreground h-3 w-3" />
                                        </span>
                                      </TooltipTrigger>
                                      <TooltipContent>
                                        <p>Determines traffic distribution between keys. Higher weights receive more requests.</p>
                                      </TooltipContent>
                                    </Tooltip>
                                  </TooltipProvider>
                                </div>
                                <Input
                                  placeholder="1.0"
                                  value={key.weight}
                                  onChange={(e) => updateKey(index, 'weight', e.target.value)}
                                  type="number"
                                  step="0.01"
                                  min="0"
                                  className={`w-20 transition-all duration-200 ease-in-out ${
                                    keysRequired && (key.weight < 0 || key.weight > 1) ? 'border-destructive' : ''
                                  }`}
                                />
                              </div>
                            </div>
                            <div>
                              <div className="flex items-center gap-2">
                                <label className="text-sm font-medium">Models (Optional)</label>
                                <TooltipProvider>
                                  <Tooltip>
                                    <TooltipTrigger asChild>
                                      <span>
                                        <Info className="text-muted-foreground h-3 w-3" />
                                      </span>
                                    </TooltipTrigger>
                                    <TooltipContent>
                                      <p>Comma-separated list of models this key applies to. Leave blank for all models.</p>
                                    </TooltipContent>
                                  </Tooltip>
                                </TooltipProvider>
                              </div>
                              <TagInput
                                placeholder="e.g. gpt-4, gpt-3.5-turbo"
                                value={key.models || []}
                                onValueChange={(newModels) => updateKey(index, 'models', newModels)}
                              />
                            </div>

                            {/* Azure Key Configuration */}
                            {selectedProvider === 'azure' && (
                              <div className="space-y-4">
                                <div>
                                  <label className="text-sm font-medium">Endpoint (Required)</label>
                                  <Input
                                    placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT"
                                    value={key.azure_key_config?.endpoint || ''}
                                    onChange={(e) => updateKeyAzureConfig(index, 'endpoint', e.target.value)}
                                    className={`transition-all duration-200 ease-in-out ${!key.azure_key_config?.endpoint?.trim() ? 'border-destructive' : ''}`}
                                  />
                                </div>
                                <div>
                                  <label className="text-sm font-medium">API Version (Optional)</label>
                                  <Input
                                    placeholder="2024-02-01 or env.AZURE_API_VERSION"
                                    value={key.azure_key_config?.api_version || ''}
                                    onChange={(e) => updateKeyAzureConfig(index, 'api_version', e.target.value)}
                                  />
                                </div>
                                <div>
                                  <label className="text-sm font-medium">Deployments (Required)</label>
                                  <div className="text-muted-foreground mb-2 text-xs">
                                    JSON object mapping model names to deployment names
                                  </div>
                                  <Textarea
                                    placeholder='{"gpt-4": "my-gpt4-deployment", "gpt-3.5-turbo": "my-gpt35-deployment"}'
                                    value={
                                      typeof key.azure_key_config?.deployments === 'string'
                                        ? key.azure_key_config.deployments
                                        : JSON.stringify(key.azure_key_config?.deployments || {}, null, 2)
                                    }
                                    onChange={(e) => {
                                      // Store as string during editing to allow intermediate invalid states
                                      updateKeyAzureConfig(index, 'deployments', e.target.value)
                                    }}
                                    onBlur={(e) => {
                                      // Try to parse as JSON on blur, but keep as string if invalid
                                      const value = e.target.value.trim()
                                      if (value) {
                                        try {
                                          const parsed = JSON.parse(value)
                                          if (typeof parsed === 'object' && parsed !== null) {
                                            updateKeyAzureConfig(index, 'deployments', parsed)
                                          }
                                        } catch {
                                          // Keep as string for validation on submit
                                        }
                                      }
                                    }}
                                    rows={3}
                                    className="wrap-anywhere max-w-full font-mono text-sm"
                                  />
                                </div>
                              </div>
                            )}

                            {/* Vertex Key Configuration */}
                            {selectedProvider === 'vertex' && (
                              <div className="space-y-4 pt-2">
                                <div>
                                  <label className="text-sm font-medium">Project ID (Required)</label>
                                  <Input
                                    placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID"
                                    value={key.vertex_key_config?.project_id || ''}
                                    onChange={(e) => updateKeyVertexConfig(index, 'project_id', e.target.value)}
                                    className={`transition-all duration-200 ease-in-out ${!key.vertex_key_config?.project_id?.trim() ? 'border-destructive' : ''}`}
                                  />
                                </div>
                                <div>
                                  <label className="text-sm font-medium">Region (Required)</label>
                                  <Input
                                    placeholder="us-central1 or env.VERTEX_REGION"
                                    value={key.vertex_key_config?.region || ''}
                                    onChange={(e) => updateKeyVertexConfig(index, 'region', e.target.value)}
                                    className={`transition-all duration-200 ease-in-out ${!key.vertex_key_config?.region?.trim() ? 'border-destructive' : ''}`}
                                  />
                                </div>
                                <div>
                                  <label className="text-sm font-medium">Auth Credentials (Required)</label>
                                  <div className="text-muted-foreground mb-2 text-xs">Service account JSON object or env.VAR_NAME</div>
                                  <Textarea
                                    placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
                                    value={key.vertex_key_config?.auth_credentials || ''}
                                    onChange={(e) => {
                                      // Always store as string - backend expects string type
                                      updateKeyVertexConfig(index, 'auth_credentials', e.target.value)
                                    }}
                                    rows={4}
                                    className={`wrap-anywhere max-w-full font-mono text-sm ${
                                      !isValidVertexAuthCredentials(key.vertex_key_config?.auth_credentials || '')
                                        ? 'border-destructive'
                                        : ''
                                    }`}
                                  />
                                  {isRedacted(key.vertex_key_config?.auth_credentials || '') && (
                                    <div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
                                      <Info className="h-3 w-3" />
                                      <span>Credentials are stored securely. Edit to update.</span>
                                    </div>
                                  )}
                                </div>
                              </div>
                            )}

                            {keys.length > 1 && (
                              <Button
                                type="button"
                                variant="destructive"
                                size="sm"
                                onClick={() => removeKey(index)}
                                className="mt-2 transition-all duration-200 ease-in-out"
                              >
                                <X className="h-4 w-4" />
                                Remove Key
                              </Button>
                            )}
                          </div>
                        ))}
                      </div>
                    </div>
                  )}

                  {/* Meta Config Tab */}
                  {selectedProvider === 'bedrock' && selectedTab === 'meta-config' && (
                    <div className="animate-in fade-in-0 slide-in-from-right-2 duration-300">
                      <MetaConfigRenderer provider={selectedProvider} metaConfig={metaConfig} onMetaConfigChange={handleMetaConfigChange} />
                    </div>
                  )}

                  {/* Network Tab */}
                  {selectedTab === 'network' && (
                    <div className="animate-in fade-in-0 slide-in-from-right-2 space-y-6 duration-300">
                      {/* Network Configuration */}
                      <div className="space-y-4">
                        <div className="flex items-center gap-2">
                          <Globe className="h-4 w-4" />
                          <h3 className="text-base font-medium">Network Configuration</h3>
                        </div>
                        <div className="grid grid-cols-1 gap-4">
                          <div>
                            <label className="text-sm font-medium">Base URL {baseURLRequired ? '(Required)' : '(Optional)'}</label>
                            <Input
                              placeholder="https://api.example.com"
                              value={networkConfig.base_url || ''}
                              onChange={(e) =>
                                updateField('networkConfig', {
                                  ...networkConfig,
                                  base_url: e.target.value,
                                })
                              }
                              className={`transition-all duration-200 ease-in-out ${baseURLRequired && !networkConfig.base_url ? 'border-destructive' : ''}`}
                            />
                          </div>
                          <div className="grid grid-cols-2 gap-4">
                            <div>
                              <label className="text-sm font-medium">Timeout (seconds)</label>
                              <Input
                                type="number"
                                placeholder="30"
                                value={networkConfig.default_request_timeout_in_seconds}
                                onChange={(e) =>
                                  updateField('networkConfig', {
                                    ...networkConfig,
                                    default_request_timeout_in_seconds: Number.parseInt(e.target.value) || 30,
                                  })
                                }
                                className="transition-all duration-200 ease-in-out"
                              />
                            </div>
                            <div>
                              <label className="text-sm font-medium">Max Retries</label>
                              <Input
                                type="number"
                                placeholder="0"
                                value={networkConfig.max_retries}
                                onChange={(e) =>
                                  updateField('networkConfig', {
                                    ...networkConfig,
                                    max_retries: Number.parseInt(e.target.value) || 0,
                                  })
                                }
                                className="transition-all duration-200 ease-in-out"
                              />
                            </div>
                          </div>
                        </div>
                      </div>

                      {/* Proxy Configuration */}
                      <div className="space-y-4">
                        <div className="flex items-center gap-2">
                          <Globe className="h-4 w-4" />
                          <h3 className="text-base font-medium">Proxy Settings</h3>
                        </div>
                        <div className="space-y-4">
                          <div className="space-y-2">
                            <label className="text-sm font-medium">Proxy Type</label>
                            <Select value={proxyConfig.type} onValueChange={(value) => updateProxyField('type', value as ProxyType)}>
                              <SelectTrigger className="w-48 transition-all duration-200 ease-in-out">
                                <SelectValue placeholder="Select type" />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value="none">None</SelectItem>
                                <SelectItem value="http">HTTP</SelectItem>
                                <SelectItem value="socks5">SOCKS5</SelectItem>
                                <SelectItem value="environment">Environment</SelectItem>
                              </SelectContent>
                            </Select>
                          </div>

                          <div
                            className="overflow-hidden transition-all duration-300 ease-in-out"
                            style={{
                              maxHeight: proxyConfig.type !== 'none' && proxyConfig.type !== 'environment' ? '300px' : '0px',
                              opacity: proxyConfig.type !== 'none' && proxyConfig.type !== 'environment' ? 1 : 0,
                            }}
                          >
                            <div className="space-y-4 pt-2">
                              <div>
                                <label className="text-sm font-medium">Proxy URL</label>
                                <Input
                                  placeholder="http://proxy.example.com"
                                  value={proxyConfig.url || ''}
                                  onChange={(e) => updateProxyField('url', e.target.value)}
                                  className="transition-all duration-200 ease-in-out"
                                />
                              </div>
                              <div className="grid grid-cols-2 gap-4">
                                <div>
                                  <label className="text-sm font-medium">Username</label>
                                  <Input
                                    value={proxyConfig.username || ''}
                                    onChange={(e) => updateProxyField('username', e.target.value)}
                                    placeholder="Proxy username"
                                    className="transition-all duration-200 ease-in-out"
                                  />
                                </div>
                                <div>
                                  <label className="text-sm font-medium">Password</label>
                                  <Input
                                    type="password"
                                    value={proxyConfig.password || ''}
                                    onChange={(e) => updateProxyField('password', e.target.value)}
                                    placeholder="Proxy password"
                                    className="transition-all duration-200 ease-in-out"
                                  />
                                </div>
                              </div>
                            </div>
                          </div>
                        </div>
                      </div>
                    </div>
                  )}

                  {/* Performance Tab */}
                  {selectedTab === 'performance' && (
                    <div className="animate-in fade-in-0 slide-in-from-right-2 space-y-2 duration-300">
                      <div className="flex items-center gap-2">
                        <Zap className="h-4 w-4" />
                        <h3 className="text-base font-medium">Performance Settings</h3>
                      </div>
                      <div
                        className="overflow-hidden transition-all duration-300 ease-in-out"
                        style={{
                          maxHeight: performanceChanged ? '200px' : '0px',
                          opacity: performanceChanged ? 1 : 0,
                        }}
                      >
                        <Alert className="mb-4">
                          <AlertTriangle className="h-4 w-4" />
                          <AlertDescription>
                            <strong>Heads up:</strong> Changing concurrency or buffer size may temporarily affect request latency for this
                            provider while the new settings are being applied.
                          </AlertDescription>
                        </Alert>
                      </div>
                      <div className="grid grid-cols-2 gap-4">
                        <div>
                          <label className="text-sm font-medium">Concurrency</label>
                          <Input
                            type="number"
                            value={performanceConfig.concurrency}
                            onChange={(e) =>
                              updateField('performanceConfig', {
                                ...performanceConfig,
                                concurrency: Number.parseInt(e.target.value) || 0,
                              })
                            }
                            className={`transition-all duration-200 ease-in-out ${!performanceValid ? 'border-destructive' : ''}`}
                          />
                        </div>
                        <div>
                          <label className="text-sm font-medium">Buffer Size</label>
                          <Input
                            type="number"
                            value={performanceConfig.buffer_size}
                            onChange={(e) =>
                              updateField('performanceConfig', {
                                ...performanceConfig,
                                buffer_size: Number.parseInt(e.target.value) || 0,
                              })
                            }
                            className={`transition-all duration-200 ease-in-out ${!performanceValid ? 'border-destructive' : ''}`}
                          />
                        </div>
                      </div>

                      <div className="mt-6 space-y-4">
                        <div className="flex items-center justify-between space-x-2">
                          <div className="space-y-0.5">
                            <label className="text-sm font-medium">Include Raw Response</label>
                            <p className="text-muted-foreground text-xs">
                              Include the raw provider response alongside the parsed response for debugging and advanced use cases
                            </p>
                          </div>
                          <Switch
                            checked={sendBackRawResponse}
                            onCheckedChange={(checked) => updateField('sendBackRawResponse', checked)}
                          />
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            </Tabs>

            {/* Form Actions */}
            <div className="bg-background sticky bottom-0 py-3">
              {availableProviders.length > 0 && (
                <div className="flex justify-end space-x-3">
                  <Button type="button" variant="outline" onClick={onCancel} className="transition-all duration-200 ease-in-out">
                    Cancel
                  </Button>
                  <TooltipProvider>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span>
                          <Button
                            type="submit"
                            disabled={!validator.isValid() || isLoading}
                            isLoading={isLoading}
                            className="transition-all duration-200 ease-in-out"
                          >
                            <Save className="h-4 w-4" />
                            {isLoading ? 'Saving...' : 'Save Provider'}
                          </Button>
                        </span>
                      </TooltipTrigger>
                      {(!validator.isValid() || isLoading) && (
                        <TooltipContent>
                          <p>{isLoading ? 'Saving...' : validator.getFirstError() || 'Please fix validation errors'}</p>
                        </TooltipContent>
                      )}
                    </Tooltip>
                  </TooltipProvider>
                </div>
              )}
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
