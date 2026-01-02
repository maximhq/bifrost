'use client'

import { useGetDevPprofQuery } from '@/lib/store'
import { isDevelopmentMode } from '@/lib/utils/port'
import { Activity, ChevronDown, ChevronUp, Cpu, HardDrive, X } from 'lucide-react'
import React, { useCallback, useMemo, useState } from 'react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

// Format bytes to human-readable string
function formatBytes (bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
}

// Format nanoseconds to human-readable string
function formatNs (ns: number): string {
  if (ns < 1000) return `${ns}ns`
  if (ns < 1000000) return `${(ns / 1000).toFixed(1)}µs`
  if (ns < 1000000000) return `${(ns / 1000000).toFixed(1)}ms`
  return `${(ns / 1000000000).toFixed(2)}s`
}

// Format timestamp to HH:MM:SS
function formatTime (timestamp: string): string {
  const date = new Date(timestamp)
  return date.toLocaleTimeString('en-US', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

// Truncate function name for display
function truncateFunction (fn: string): string {
  const parts = fn.split('/')
  const last = parts[parts.length - 1]
  if (last.length > 40) {
    return '...' + last.slice(-37)
  }
  return last
}

export function DevProfiler (): React.ReactNode {
  const [isVisible, setIsVisible] = useState(true)
  const [isExpanded, setIsExpanded] = useState(true)
  const [isDismissed, setIsDismissed] = useState(false)

  // Only fetch in development mode and when not dismissed
  const shouldFetch = isDevelopmentMode() && !isDismissed

  const { data, isLoading, error } = useGetDevPprofQuery(undefined, {
    pollingInterval: shouldFetch ? 10000 : 0, // Poll every 10 seconds
    skip: !shouldFetch,
  })

  // Memoize chart data transformation
  const memoryChartData = useMemo(() => {
    if (!data?.history) return []
    return data.history.map((point) => ({
      time: formatTime(point.timestamp),
      alloc: point.alloc / (1024 * 1024), // Convert to MB
      heapInuse: point.heap_inuse / (1024 * 1024),
    }))
  }, [data?.history])

  const cpuChartData = useMemo(() => {
    if (!data?.history) return []
    return data.history.map((point) => ({
      time: formatTime(point.timestamp),
      cpuPercent: point.cpu_percent,
      goroutines: point.goroutines,
    }))
  }, [data?.history])

  const handleDismiss = useCallback(() => {
    setIsDismissed(true)
  }, [])

  const handleToggleExpand = useCallback(() => {
    setIsExpanded((prev) => !prev)
  }, [])

  const handleToggleVisible = useCallback(() => {
    setIsVisible((prev) => !prev)
  }, [])

  // Don't render in production mode or if dismissed
  if (!isDevelopmentMode() || isDismissed) {
    return null
  }

  // Minimized state - just show a small button
  if (!isVisible) {
    return (
      <button
        onClick={handleToggleVisible}
        className="fixed right-4 bottom-4 z-50 flex h-10 w-10 items-center justify-center rounded-full bg-zinc-900 text-white shadow-lg transition-all hover:bg-zinc-800"
        title="Show Dev Profiler"
      >
        <Activity className="h-5 w-5" />
      </button>
    )
  }

  return (
    <div className="fixed right-4 bottom-4 z-50 w-[420px] overflow-hidden rounded-lg border border-zinc-700 bg-zinc-900 font-mono text-xs text-zinc-100 shadow-2xl">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-zinc-700 bg-zinc-800 px-3 py-2">
        <div className="flex items-center gap-2">
          <span className="font-semibold text-emerald-400">Dev Profiler</span>
          {isLoading && (
            <span className="ml-2 h-2 w-2 animate-pulse rounded-full bg-amber-400" />
          )}
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={handleToggleExpand}
            className="rounded p-1 transition-colors hover:bg-zinc-700"
            title={isExpanded ? 'Collapse' : 'Expand'}
          >
            {isExpanded ? (
              <ChevronDown className="h-4 w-4" />
            ) : (
              <ChevronUp className="h-4 w-4" />
            )}
          </button>
          <button
            onClick={handleToggleVisible}
            className="rounded p-1 transition-colors hover:bg-zinc-700"
            title="Minimize"
          >
            <ChevronDown className="h-4 w-4" />
          </button>
          <button
            onClick={handleDismiss}
            className="rounded p-1 transition-colors hover:bg-zinc-700"
            title="Dismiss"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      </div>

      {Boolean(error) && (
        <div className="border-b border-zinc-700 bg-red-900/30 px-3 py-2 text-red-300">
          Failed to load profiling data
        </div>
      )}

      {isExpanded && data && (
        <div className="max-h-[70vh] overflow-y-auto">
          {/* Current Stats */}
          <div className="grid grid-cols-3 gap-2 border-b border-zinc-700 p-3">
            <div className="flex flex-col">
              <span className="text-zinc-500">CPU Usage</span>
              <span className="font-semibold text-orange-400">
                {data.cpu.usage_percent.toFixed(1)}%
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-zinc-500">Heap Alloc</span>
              <span className="font-semibold text-cyan-400">
                {formatBytes(data.memory.alloc)}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-zinc-500">Heap In-Use</span>
              <span className="font-semibold text-blue-400">
                {formatBytes(data.memory.heap_inuse)}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-zinc-500">System</span>
              <span className="font-semibold text-purple-400">
                {formatBytes(data.memory.sys)}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-zinc-500">Goroutines</span>
              <span className="font-semibold text-emerald-400">
                {data.runtime.num_goroutine}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-zinc-500">GC Pause</span>
              <span className="font-semibold text-amber-400">
                {formatNs(data.runtime.gc_pause_ns)}
              </span>
            </div>
          </div>

          {/* CPU Chart */}
          <div className="border-b border-zinc-700 p-3">
            <div className="mb-2 flex items-center gap-2">
              <Cpu className="h-3 w-3 text-orange-400" />
              <span className="text-zinc-400">CPU Usage (last 5 min)</span>
            </div>
            <div className="h-24">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={cpuChartData}>
                  <defs>
                    <linearGradient id="cpuGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#f97316" stopOpacity={0.3} />
                      <stop offset="95%" stopColor="#f97316" stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="goroutineGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#34d399" stopOpacity={0.3} />
                      <stop offset="95%" stopColor="#34d399" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="#3f3f46" />
                  <XAxis
                    dataKey="time"
                    tick={{ fill: '#71717a', fontSize: 9 }}
                    tickLine={false}
                    axisLine={false}
                  />
                  <YAxis
                    yAxisId="left"
                    tick={{ fill: '#71717a', fontSize: 9 }}
                    tickLine={false}
                    axisLine={false}
                    tickFormatter={(v) => `${Number(v).toFixed(0)}%`}
                    width={35}
                    domain={[0, 'auto']}
                  />
                  <YAxis
                    yAxisId="right"
                    orientation="right"
                    tick={{ fill: '#71717a', fontSize: 9 }}
                    tickLine={false}
                    axisLine={false}
                    width={30}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#18181b',
                      border: '1px solid #3f3f46',
                      borderRadius: '6px',
                      fontSize: '10px',
                    }}
                    labelStyle={{ color: '#a1a1aa' }}
                  />
                  <Area
                    type="monotone"
                    dataKey="cpuPercent"
                    stroke="#f97316"
                    strokeWidth={1.5}
                    fill="url(#cpuGradient)"
                    yAxisId="left"
                    name="CPU %"
                  />
                  <Area
                    type="monotone"
                    dataKey="goroutines"
                    stroke="#34d399"
                    strokeWidth={1.5}
                    fill="url(#goroutineGradient)"
                    yAxisId="right"
                    name="Goroutines"
                  />
                </AreaChart>
              </ResponsiveContainer>
            </div>
            <div className="mt-1 flex gap-4 text-[10px]">
              <span className="flex items-center gap-1">
                <span className="h-2 w-2 rounded-full bg-orange-500" />
                CPU %
              </span>
              <span className="flex items-center gap-1">
                <span className="h-2 w-2 rounded-full bg-emerald-400" />
                Goroutines
              </span>
            </div>
          </div>

          {/* Memory Chart */}
          <div className="border-b border-zinc-700 p-3">
            <div className="mb-2 flex items-center gap-2">
              <HardDrive className="h-3 w-3 text-cyan-400" />
              <span className="text-zinc-400">Memory (last 5 min)</span>
            </div>
            <div className="h-24">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={memoryChartData}>
                  <defs>
                    <linearGradient id="allocGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#22d3ee" stopOpacity={0.3} />
                      <stop offset="95%" stopColor="#22d3ee" stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="heapGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.3} />
                      <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="#3f3f46" />
                  <XAxis
                    dataKey="time"
                    tick={{ fill: '#71717a', fontSize: 9 }}
                    tickLine={false}
                    axisLine={false}
                  />
                  <YAxis
                    tick={{ fill: '#71717a', fontSize: 9 }}
                    tickLine={false}
                    axisLine={false}
                    tickFormatter={(v) => `${Number(v).toFixed(0)}MB`}
                    width={45}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#18181b',
                      border: '1px solid #3f3f46',
                      borderRadius: '6px',
                      fontSize: '10px',
                    }}
                    labelStyle={{ color: '#a1a1aa' }}
                  />
                  <Area
                    type="monotone"
                    dataKey="alloc"
                    stroke="#22d3ee"
                    strokeWidth={1.5}
                    fill="url(#allocGradient)"
                    name="Alloc"
                  />
                  <Area
                    type="monotone"
                    dataKey="heapInuse"
                    stroke="#3b82f6"
                    strokeWidth={1.5}
                    fill="url(#heapGradient)"
                    name="Heap In-Use"
                  />
                </AreaChart>
              </ResponsiveContainer>
            </div>
            <div className="mt-1 flex gap-4 text-[10px]">
              <span className="flex items-center gap-1">
                <span className="h-2 w-2 rounded-full bg-cyan-400" />
                Alloc
              </span>
              <span className="flex items-center gap-1">
                <span className="h-2 w-2 rounded-full bg-blue-500" />
                Heap In-Use
              </span>
            </div>
          </div>

          {/* Top Allocations */}
          <div className="p-3">
            <div className="mb-2 flex items-center gap-2">
              <HardDrive className="h-3 w-3 text-rose-400" />
              <span className="text-zinc-400">Top Allocations</span>
            </div>
            <div className="space-y-1">
              {(data.top_allocations ?? []).map((alloc, i) => (
                <div
                  key={i}
                  className="flex items-center justify-between rounded bg-zinc-800 px-2 py-1"
                >
                  <div className="flex flex-col overflow-hidden">
                    <span
                      className="truncate text-zinc-300"
                      title={alloc.function}
                    >
                      {truncateFunction(alloc.function)}
                    </span>
                    <span className="text-[10px] text-zinc-500">
                      {alloc.file}:{alloc.line}
                    </span>
                  </div>
                  <div className="flex flex-col items-end">
                    <span className="text-rose-400">
                      {formatBytes(alloc.bytes)}
                    </span>
                    <span className="text-[10px] text-zinc-500">
                      {alloc.count.toLocaleString()} allocs
                    </span>
                  </div>
                </div>
              ))}
            </div>
          </div>

          {/* Footer with info */}
          <div className="border-t border-zinc-700 bg-zinc-800 px-3 py-2 text-[10px] text-zinc-500">
            CPUs: {data.runtime.num_cpu} | GOMAXPROCS: {data.runtime.gomaxprocs} |
            GC: {data.runtime.num_gc} | Objects: {data.memory.heap_objects.toLocaleString()}
          </div>
        </div>
      )}

      {/* Collapsed state */}
      {!isExpanded && data && (
        <div className="flex items-center justify-between bg-zinc-800/50 px-3 py-2">
          <span className="text-orange-400">
            CPU: {data.cpu.usage_percent.toFixed(1)}%
          </span>
          <span className="text-zinc-400">
            Heap: {formatBytes(data.memory.heap_inuse)}
          </span>
          <span className="text-zinc-400">
            Goroutines: {data.runtime.num_goroutine}
          </span>
        </div>
      )}
    </div>
  )
}
