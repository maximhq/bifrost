'use client'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { ConcurrencyAndBufferSize } from '@/lib/types/config'
import { cn } from '@/lib/utils'
import { AlertTriangle } from 'lucide-react'

interface PerformanceTabProps {
  performanceConfig: ConcurrencyAndBufferSize
  sendBackRawResponse: boolean
  performanceChanged: boolean
  performanceValid: boolean
  onUpdatePerformanceConfig: (config: ConcurrencyAndBufferSize) => void
  onUpdateSendBackRawResponse: (value: boolean) => void
}

export function PerformanceTab({
  performanceConfig,
  sendBackRawResponse,
  performanceChanged,
  performanceValid,
  onUpdatePerformanceConfig,
  onUpdateSendBackRawResponse
}: PerformanceTabProps) {
  return (
    <div className="space-y-2">
      <div className={cn('overflow-hidden', !performanceChanged && 'hidden')}>
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
          <label className="mb-2 block text-sm font-medium">Concurrency</label>
          <Input
            type="number"
            value={performanceConfig.concurrency}
            onChange={(e) =>
              onUpdatePerformanceConfig({
                ...performanceConfig,
                concurrency: Number.parseInt(e.target.value) || 1
              })
            }
            className={`${!performanceValid ? 'border-destructive' : ''}`}
          />
        </div>
        <div>
          <label className="mb-2 block text-sm font-medium">Buffer Size</label>
          <Input
            type="number"
            value={performanceConfig.buffer_size}
            onChange={(e) =>
              onUpdatePerformanceConfig({
                ...performanceConfig,
                buffer_size: Number.parseInt(e.target.value) || 10
              })
            }
            className={`${!performanceValid ? 'border-destructive' : ''}`}
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
            size="md"
            checked={sendBackRawResponse}
            onCheckedChange={(checked) => onUpdateSendBackRawResponse(checked)}
          />
        </div>
      </div>
    </div>
  )
}
