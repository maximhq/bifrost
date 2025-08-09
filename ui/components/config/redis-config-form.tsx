'use client'

import { useState, useEffect, useRef } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { RedisConfig } from '@/lib/types/config'
import { apiService } from '@/lib/api'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'

const defaultRedisConfig: RedisConfig = {
  addr: 'localhost:6379',
  username: '',
  password: '',
  db: 0,
  ttl_seconds: 300,
  prefix: '',
  cache_by_model: true,
  cache_by_provider: true,
}

export default function RedisConfigForm() {
  const [config, setConfig] = useState<RedisConfig>(defaultRedisConfig)
  const [loading, setLoading] = useState(false)

  // Use refs to store timeout IDs for debounced updates
  const addrTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)
  const usernameTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)
  const passwordTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)
  const prefixTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)
  const ttlTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)

  useEffect(() => {
    fetchRedisConfig()
  }, [])

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      if (addrTimeoutRef.current) clearTimeout(addrTimeoutRef.current)
      if (usernameTimeoutRef.current) clearTimeout(usernameTimeoutRef.current)
      if (passwordTimeoutRef.current) clearTimeout(passwordTimeoutRef.current)
      if (prefixTimeoutRef.current) clearTimeout(prefixTimeoutRef.current)
      if (ttlTimeoutRef.current) clearTimeout(ttlTimeoutRef.current)
    }
  }, [])

  const fetchRedisConfig = async () => {
    setLoading(true)
    const [response, error] = await apiService.getRedisConfig()
    if (error) {
      toast.error(error)
    } else if (response) {
      setConfig({
        ...defaultRedisConfig,
        ...response,
      })
    }
    setLoading(false)
  }

  const updateConfig = async (updates: Partial<RedisConfig>) => {
    const newConfig = { ...config, ...updates }
    setConfig(newConfig)

    try {
      const [_, error] = await apiService.updateRedisConfig(newConfig)
      if (error) {
        if (error.includes('status code 405')) {
          toast.error('Please enable redis plugin and restart Bifrost.')
          return
        }
        toast.error(error)
        // Revert on error
        setConfig(config)
      } else {
        toast.success('Redis configuration updated successfully')
      }
    } catch (error) {
      toast.error('Failed to update Redis configuration')
      // Revert on error
      setConfig(config)
    }
  }

  // Debounced update functions for string fields
  const handleAddrChange = (value: string) => {
    setConfig((prev) => ({ ...prev, addr: value }))

    // Clear existing timeout
    if (addrTimeoutRef.current) {
      clearTimeout(addrTimeoutRef.current)
    }

    // Set new timeout
    addrTimeoutRef.current = setTimeout(() => {
      updateConfig({ addr: value })
    }, 1000)
  }

  const handleUsernameChange = (value: string) => {
    setConfig((prev) => ({ ...prev, username: value }))

    // Clear existing timeout
    if (usernameTimeoutRef.current) {
      clearTimeout(usernameTimeoutRef.current)
    }

    // Set new timeout
    usernameTimeoutRef.current = setTimeout(() => {
      updateConfig({ username: value })
    }, 1000)
  }

  const handlePasswordChange = (value: string) => {
    setConfig((prev) => ({ ...prev, password: value }))

    // Clear existing timeout
    if (passwordTimeoutRef.current) {
      clearTimeout(passwordTimeoutRef.current)
    }

    // Set new timeout
    passwordTimeoutRef.current = setTimeout(() => {
      updateConfig({ password: value })
    }, 1000)
  }

  const handlePrefixChange = (value: string) => {
    setConfig((prev) => ({ ...prev, prefix: value }))

    // Clear existing timeout
    if (prefixTimeoutRef.current) {
      clearTimeout(prefixTimeoutRef.current)
    }

    // Set new timeout
    prefixTimeoutRef.current = setTimeout(() => {
      updateConfig({ prefix: value })
    }, 1000)
  }

  const handleTtlChange = (value: number) => {
    setConfig((prev) => ({ ...prev, ttl_seconds: value }))

    // Clear existing timeout
    if (ttlTimeoutRef.current) {
      clearTimeout(ttlTimeoutRef.current)
    }

    // Set new timeout
    ttlTimeoutRef.current = setTimeout(() => {
      updateConfig({ ttl_seconds: value })
    }, 1000)
  }

  if (loading) {
    return (
      <Card>
        <CardContent className="p-6">
          <div className="text-muted-foreground">Loading Redis configuration...</div>
        </CardContent>
      </Card>
    )
  }

  return (
    <div>
      <div className="space-y-6">
        {/* Connection Settings */}
        <div className="space-y-4">
          <h3 className="text-sm font-medium">Connection Settings</h3>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="addr">Redis Address *</Label>
              <Input id="addr" placeholder="localhost:6379" value={config.addr} onChange={(e) => handleAddrChange(e.target.value)} />
            </div>
            <div className="space-y-2">
              <Label htmlFor="db">Database Number</Label>
              <Input
                id="db"
                type="number"
                min="0"
                value={config.db}
                onChange={(e) => updateConfig({ db: parseInt(e.target.value) || 0 })}
              />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                placeholder="Optional"
                value={config.username || ''}
                onChange={(e) => handleUsernameChange(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                placeholder="Optional"
                value={config.password || ''}
                onChange={(e) => handlePasswordChange(e.target.value)}
              />
            </div>
          </div>
        </div>

        {/* Cache Settings */}
        <div className="space-y-4">
          <h3 className="text-sm font-medium">Cache Settings</h3>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="ttl">TTL (seconds)</Label>
              <Input
                id="ttl"
                type="number"
                min="1"
                value={config.ttl_seconds}
                onChange={(e) => handleTtlChange(parseInt(e.target.value) || 300)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="prefix">Key Prefix</Label>
              <Input id="prefix" placeholder="Optional" value={config.prefix || ''} onChange={(e) => handlePrefixChange(e.target.value)} />
            </div>
          </div>
        </div>

        {/* Cache Behavior */}
        <div className="space-y-4">
          <h3 className="text-sm font-medium">Cache Behavior</h3>
          <div className="space-y-3">
            <div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
              <div className="space-y-0.5">
                <Label className="text-sm font-medium">Cache by Model</Label>
                <p className="text-muted-foreground text-xs">Include model name in cache key</p>
              </div>
              <Switch checked={config.cache_by_model} onCheckedChange={(checked) => updateConfig({ cache_by_model: checked })} size="md" />
            </div>
            <div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
              <div className="space-y-0.5">
                <Label className="text-sm font-medium">Cache by Provider</Label>
                <p className="text-muted-foreground text-xs">Include provider name in cache key</p>
              </div>
              <Switch
                checked={config.cache_by_provider}
                onCheckedChange={(checked) => updateConfig({ cache_by_provider: checked })}
                size="md"
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
