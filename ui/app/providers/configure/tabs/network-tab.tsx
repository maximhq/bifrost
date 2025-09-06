'use client'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { NetworkConfig, ProxyConfig, ProxyType } from '@/lib/types/config'
import { cn } from '@/lib/utils'
import { AlertTriangle } from 'lucide-react'

interface NetworkTabProps {
  networkConfig: NetworkConfig
  proxyConfig: ProxyConfig
  baseURLRequired: boolean
  networkChanged: boolean
  onUpdateNetworkConfig: (config: NetworkConfig) => void
  onUpdateProxyConfig: (config: ProxyConfig) => void
}

export function NetworkTab({
  networkConfig,
  proxyConfig,
  baseURLRequired,
  networkChanged,
  onUpdateNetworkConfig,
  onUpdateProxyConfig
}: NetworkTabProps) {
  const updateProxyField = <K extends keyof ProxyConfig>(field: K, value: ProxyConfig[K]) => {
    onUpdateProxyConfig({ ...proxyConfig, [field]: value })
  }

  return (
    <div className="space-y-6">
      <div className={cn('hidden', networkChanged && 'block')}>
        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertDescription>
            The settings below require a Bifrost service restart to take effect. Current connections will continue with
            existing settings until restart.
          </AlertDescription>
        </Alert>
      </div>

      {/* Network Configuration */}
      <div className="space-y-4">
        <div className="grid grid-cols-1 gap-4">
          <div>
            <label className="mb-2 block text-sm font-medium">Base URL {baseURLRequired ? '(Required)' : '(Optional)'}</label>
            <Input
              placeholder="https://api.example.com"
              value={networkConfig.base_url || ''}
              onChange={(e) =>
                onUpdateNetworkConfig({
                  ...networkConfig,
                  base_url: e.target.value
                })
              }
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="mb-2 block text-sm font-medium">Timeout (seconds)</label>
              <Input
                type="number"
                placeholder="30"
                value={networkConfig.default_request_timeout_in_seconds}
                onChange={(e) => {
                  onUpdateNetworkConfig({
                    ...networkConfig,
                    default_request_timeout_in_seconds: Number.parseInt(e.target.value)
                  })
                }}
                min={1}
              />
            </div>
            <div>
              <label className="mb-2 block text-sm font-medium">Max Retries</label>
              <Input
                type="number"
                placeholder="0"
                value={networkConfig.max_retries}
                onChange={(e) =>
                  onUpdateNetworkConfig({
                    ...networkConfig,
                    max_retries: Number.parseInt(e.target.value) || 0
                  })
                }
                min={0}
              />
            </div>
          </div>
        </div>
      </div>

      {/* Proxy Configuration */}
      <div className="space-y-4">
        <div className="space-y-4">
          <div>
            <label className="mb-2 block text-sm font-medium">Proxy Type</label>
            <Select value={proxyConfig.type} onValueChange={(value) => updateProxyField('type', value as ProxyType)}>
              <SelectTrigger className="w-48">
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
            className={cn('overflow-hidden', (proxyConfig.type === 'none' || proxyConfig.type === 'environment') && 'hidden')}
          >
            <div className="space-y-4 pt-2">
              <div>
                <label className="mb-2 block text-sm font-medium">Proxy URL</label>
                <Input
                  placeholder="http://proxy.example.com"
                  value={proxyConfig.url || ''}
                  onChange={(e) => updateProxyField('url', e.target.value)}
                />
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="mb-2 block text-sm font-medium">Username</label>
                  <Input
                    value={proxyConfig.username || ''}
                    onChange={(e) => updateProxyField('username', e.target.value)}
                    placeholder="Proxy username"
                  />
                </div>
                <div>
                  <label className="mb-2 block text-sm font-medium">Password</label>
                  <Input
                    type="password"
                    value={proxyConfig.password || ''}
                    onChange={(e) => updateProxyField('password', e.target.value)}
                    placeholder="Proxy password"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
