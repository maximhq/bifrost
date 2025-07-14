'use client'

import { useState, useEffect, useCallback, useRef } from 'react'
import { CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { CoreConfig } from '@/lib/types/config'
import { apiService } from '@/lib/api'
import { toast } from 'sonner'
import { Separator } from '@radix-ui/react-separator'
import { Alert, AlertDescription } from '../ui/alert'
import { Input } from '../ui/input'
import { Textarea } from '../ui/textarea'
import { parseArrayFromText } from '@/lib/utils/array'

export default function CoreSettingsList() {
  const [config, setConfig] = useState<CoreConfig>({
    drop_excess_requests: false,
    initial_pool_size: 300,
    enable_logging: true,
  })
  const [droppedRequests, setDroppedRequests] = useState<number>(0)
  const [isLoading, setIsLoading] = useState(true)
  const [localValues, setLocalValues] = useState<{
    initial_pool_size: string
    prometheus_labels: string
  }>({
    initial_pool_size: '300',
    prometheus_labels: '',
  })

  useEffect(() => {
    const fetchDroppedRequests = async () => {
      const [response, error] = await apiService.getDroppedRequests()
      if (error) {
        toast.error(error)
      } else if (response) {
        setDroppedRequests(response.dropped_requests)
      }
    }
    fetchDroppedRequests()
  }, [])

  // Use refs to store timeout IDs
  const poolSizeTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)
  const prometheusLabelsTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)

  useEffect(() => {
    const fetchConfig = async () => {
      const [coreConfig, error] = await apiService.getCoreConfig()
      if (error) {
        toast.error(error)
      } else if (coreConfig) {
        setConfig(coreConfig)
        setLocalValues({
          initial_pool_size: coreConfig.initial_pool_size?.toString() || '300',
          prometheus_labels: coreConfig.prometheus_labels || '',
        })
      }
      setIsLoading(false)
    }
    fetchConfig()
  }, [])

  const updateConfig = useCallback(
    async (field: keyof CoreConfig, value: boolean | number | string[]) => {
      const newConfig = { ...config, [field]: value }
      setConfig(newConfig)

      const [, error] = await apiService.updateCoreConfig(newConfig)
      if (error) {
        toast.error(error)
      } else {
        toast.success('Core setting updated successfully.')
      }
    },
    [config],
  )

  const handleConfigChange = async (field: keyof CoreConfig, value: boolean | number | string[]) => {
    await updateConfig(field, value)
  }

  const handlePoolSizeChange = useCallback(
    (value: string) => {
      setLocalValues((prev) => ({ ...prev, initial_pool_size: value }))

      // Clear existing timeout
      if (poolSizeTimeoutRef.current) {
        clearTimeout(poolSizeTimeoutRef.current)
      }

      // Set new timeout
      poolSizeTimeoutRef.current = setTimeout(() => {
        const numValue = Number.parseInt(value)
        if (!isNaN(numValue) && numValue > 0) {
          updateConfig('initial_pool_size', numValue)
        }
      }, 1000)
    },
    [updateConfig],
  )

  const handlePrometheusLabelsChange = useCallback(
    (value: string) => {
      setLocalValues((prev) => ({ ...prev, prometheus_labels: value }))

      // Clear existing timeout
      if (prometheusLabelsTimeoutRef.current) {
        clearTimeout(prometheusLabelsTimeoutRef.current)
      }

      // Set new timeout
      prometheusLabelsTimeoutRef.current = setTimeout(() => {
        updateConfig('prometheus_labels', parseArrayFromText(value))
      }, 1000)
    },
    [updateConfig],
  )

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      if (poolSizeTimeoutRef.current) {
        clearTimeout(poolSizeTimeoutRef.current)
      }
      if (prometheusLabelsTimeoutRef.current) {
        clearTimeout(prometheusLabelsTimeoutRef.current)
      }
    }
  }, [])

  if (isLoading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin" />
      </div>
    )
  }

  return (
    <div>
      <CardHeader className="mb-4 px-0">
        <CardTitle className="flex items-center gap-2">Core System Settings</CardTitle>
        <CardDescription>Configure core Bifrost settings like request handling, pool sizes, and system behavior.</CardDescription>
      </CardHeader>
      <div className="space-y-6">
        {/* Drop Excess Requests */}
        <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="drop-excess-requests" className="text-sm font-medium">
              Drop Excess Requests
            </label>
            <p className="text-muted-foreground text-sm">If enabled, Bifrost will drop requests that exceed pool capacity.</p>
          </div>
          <Switch
            id="drop-excess-requests"
            checked={config.drop_excess_requests}
            onCheckedChange={(checked) => handleConfigChange('drop_excess_requests', checked)}
          />
        </div>

        <Separator />

        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertDescription>
            The settings below require a Bifrost service restart to take effect. Current connections will continue with existing settings
            until restart.
          </AlertDescription>
        </Alert>

        <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="initial-pool-size" className="text-sm font-medium">
              Initial Pool Size
            </label>
            <p className="text-muted-foreground text-sm">The initial connection pool size.</p>
          </div>
          <Input
            id="initial-pool-size"
            type="number"
            className="w-24"
            value={localValues.initial_pool_size}
            onChange={(e) => handlePoolSizeChange(e.target.value)}
            min="1"
          />
        </div>

        <div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="enable-logging" className="text-sm font-medium">
              Enable Logs
            </label>
            <p className="text-muted-foreground text-sm">
              Enable logging of requests and responses to a SQLite database. This can add 40-60mb of overhead to the system memory.
            </p>
          </div>
          <Switch
            id="enable-logging"
            checked={config.enable_logging}
            onCheckedChange={(checked) => handleConfigChange('enable_logging', checked)}
          />
        </div>

        <div className="space-y-2 rounded-lg border p-4">
          <div className="space-y-0.5">
            <label htmlFor="prometheus-labels" className="text-sm font-medium">
              Prometheus Labels
            </label>
            <p className="text-muted-foreground text-sm">Comma-separated list of custom labels to add to the Prometheus metrics.</p>
          </div>
          <Textarea
            id="prometheus-labels"
            className="h-24"
            placeholder="teamId, projectId, environment"
            value={localValues.prometheus_labels}
            onChange={(e) => handlePrometheusLabelsChange(e.target.value)}
          />
        </div>
      </div>
    </div>
  )
}
