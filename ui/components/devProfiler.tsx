'use client'

import { useGetDevGoroutinesQuery, useGetDevPprofQuery } from '@/lib/store'
import type { GoroutineGroup } from '@/lib/store/apis/devApi'
import { isDevelopmentMode } from '@/lib/utils/port'
import { Activity, AlertTriangle, ChevronDown, ChevronRight, ChevronUp, Cpu, EyeOff, HardDrive, RotateCcw, TrendingUp, X } from 'lucide-react'
import React, { useCallback, useEffect, useMemo, useState } from 'react'
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

// Get category badge color
function getCategoryColor (category: string): string {
  switch (category) {
    case 'per-request':
      return 'text-amber-400 bg-amber-400/10'
    case 'background':
      return 'text-blue-400 bg-blue-400/10'
    default:
      return 'text-zinc-400 bg-zinc-400/10'
  }
}

// Extract file path from stack (first line containing .go:)
function getStackFilePath (stack: string[]): string {
  for (const line of stack) {
    // Match file path like "/path/to/file.go:123" and extract just the path
    const match = line.match(/^\s*([^\s]+\.go):\d+/)
    if (match) {
      return match[1]
    }
  }
  return ''
}

// Generate a stable ID for a goroutine group
function getGoroutineId (g: GoroutineGroup): string {
  return `${g.top_func}::${g.state}::${g.count}::${g.wait_minutes ?? 0}`
}

// localStorage key for skipped goroutine file paths
const SKIPPED_GOROUTINE_FILES_KEY = 'devProfiler.skippedGoroutineFiles'
const PROFILER_VISIBLE_KEY = 'devProfiler.isVisible'
const PROFILER_EXPANDED_KEY = 'devProfiler.isExpanded'

// Load skipped goroutine file paths from localStorage
function loadSkippedGoroutineFiles (): Set<string> {
  if (typeof window === 'undefined') return new Set()
  try {
    const stored = localStorage.getItem(SKIPPED_GOROUTINE_FILES_KEY)
    return stored ? new Set(JSON.parse(stored)) : new Set()
  } catch {
    return new Set()
  }
}

// Save skipped goroutine file paths to localStorage
function saveSkippedGoroutineFiles (skipped: Set<string>): void {
  if (typeof window === 'undefined') return
  try {
    localStorage.setItem(SKIPPED_GOROUTINE_FILES_KEY, JSON.stringify([...skipped]))
  } catch {
    // Ignore storage errors
  }
}

function loadBooleanFromStorage (key: string, defaultValue: boolean): boolean {
  if (typeof window === 'undefined') return defaultValue
  try {
    const stored = localStorage.getItem(key)
    if (stored === null) return defaultValue
    if (stored === 'true') return true
    if (stored === 'false') return false
    return defaultValue
  } catch {
    return defaultValue
  }
}

function saveBooleanToStorage (key: string, value: boolean): void {
  if (typeof window === 'undefined') return
  try {
    localStorage.setItem(key, value ? 'true' : 'false')
  } catch {
    // Ignore storage errors
  }
}

// Goroutine Health Section subcomponent
interface GoroutineHealthProps {
  goroutineData: {
    summary: {
      background: number
      per_request: number
      long_waiting: number
      potentially_stuck: number
    }
    total_goroutines: number
  } | undefined
  goroutineHealth: 'healthy' | 'warning' | 'critical'
  goroutineTrend: {
    isGrowing: boolean
    growthPercent: number
    avg: number
  } | null
  problemGoroutines: GoroutineGroup[]
  expandedGoroutines: Set<string>
  toggleGoroutineExpand: (id: string) => void
  skippedGoroutines: Set<string>
  onSkipGoroutine: (topFunc: string) => void
  onClearSkipped: () => void
}

function GoroutineHealthSection ({
  goroutineData,
  goroutineHealth,
  goroutineTrend,
  problemGoroutines,
  expandedGoroutines,
  toggleGoroutineExpand,
  skippedGoroutines,
  onSkipGoroutine,
  onClearSkipped,
}: GoroutineHealthProps): React.ReactNode {
  if (!goroutineData) return null

  const { summary, total_goroutines } = goroutineData

  return (
    <div className="p-3">
      {/* Header with health status */}
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Activity className="h-3 w-3 text-emerald-400" />
          <span className="text-zinc-400">Goroutine Health</span>
        </div>
        <div className="flex items-center gap-2">
          {goroutineTrend?.isGrowing && (
            <span className="flex items-center gap-1 text-amber-400" title="Goroutine count growing">
              <TrendingUp className="h-3 w-3" />
              <span className="text-[10px]">+{goroutineTrend.growthPercent.toFixed(0)}%</span>
            </span>
          )}
          {goroutineHealth === 'critical' && (
            <span className="flex items-center gap-1 rounded bg-red-500/20 px-1.5 py-0.5 text-[10px] text-red-400">
              <AlertTriangle className="h-3 w-3" />
              Stuck
            </span>
          )}
          {goroutineHealth === 'warning' && (
            <span className="flex items-center gap-1 rounded bg-amber-500/20 px-1.5 py-0.5 text-[10px] text-amber-400">
              <AlertTriangle className="h-3 w-3" />
              Long Wait
            </span>
          )}
          {goroutineHealth === 'healthy' && (
            <span className="rounded bg-emerald-500/20 px-1.5 py-0.5 text-[10px] text-emerald-400">
              Healthy
            </span>
          )}
        </div>
      </div>

      {/* Summary stats */}
      <div className="mb-2 grid grid-cols-4 gap-2 rounded bg-zinc-800/50 p-2">
        <div className="flex flex-col items-center">
          <span className="text-[10px] text-zinc-500">Total</span>
          <span className="font-semibold text-emerald-400">{total_goroutines}</span>
        </div>
        <div className="flex flex-col items-center">
          <span className="text-[10px] text-zinc-500">Background</span>
          <span className="font-semibold text-blue-400">{summary.background}</span>
        </div>
        <div className="flex flex-col items-center">
          <span className="text-[10px] text-zinc-500">Per-Request</span>
          <span className="font-semibold text-amber-400">{summary.per_request}</span>
        </div>
        <div className="flex flex-col items-center">
          <span className="text-[10px] text-zinc-500">Stuck</span>
          <span className={`font-semibold ${summary.potentially_stuck > 0 ? 'text-red-400' : 'text-zinc-500'}`}>
            {summary.potentially_stuck}
          </span>
        </div>
      </div>

      {/* Problem goroutines list */}
      {(problemGoroutines.length > 0 || skippedGoroutines.size > 0) && (
        <div className="space-y-1">
          <div className="flex items-center justify-between">
            <span className="text-[10px] text-zinc-500">Potential Leaks</span>
            {skippedGoroutines.size > 0 && (
              <button
                onClick={onClearSkipped}
                className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] text-zinc-400 hover:bg-zinc-700 hover:text-zinc-200"
                title="Clear all hidden goroutines"
              >
                <RotateCcw className="h-2.5 w-2.5" />
                {skippedGoroutines.size} hidden
              </button>
            )}
          </div>
          {problemGoroutines.map((g) => {
            const gid = getGoroutineId(g)
            return (
            <div key={gid} className="group relative rounded bg-zinc-800">
              <div
                role="button"
                tabIndex={0}
                onClick={() => toggleGoroutineExpand(gid)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    toggleGoroutineExpand(gid)
                  }
                }}
                className="flex w-full cursor-pointer flex-col gap-1 px-2 py-1.5 pr-8 text-left hover:bg-zinc-700/50"
              >
                <div className="flex w-full items-center gap-2">
                  {expandedGoroutines.has(gid) ? (
                    <ChevronDown className="h-3 w-3 shrink-0 text-zinc-500" />
                  ) : (
                    <ChevronRight className="h-3 w-3 shrink-0 text-zinc-500" />
                  )}
                  <span className="min-w-0 flex-1 break-all text-zinc-300" title={g.top_func}>
                    {truncateFunction(g.top_func)}
                  </span>
                </div>
                <div className="flex items-center gap-2 pl-5 text-[10px]">
                  <span className={`rounded px-1 py-0.5 ${getCategoryColor(g.category)}`}>
                    {g.category}
                  </span>
                  <span className="text-zinc-500">{g.count}x</span>
                  {g.wait_minutes != null && (
                    <span className="text-amber-400">{g.wait_minutes}m waiting</span>
                  )}
                </div>
              </div>
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  const filePath = getStackFilePath(g.stack)
                  if (filePath) onSkipGoroutine(filePath)
                }}
                className="absolute right-1 top-1.5 shrink-0 rounded p-1 text-zinc-500 opacity-0 transition-opacity hover:bg-zinc-600 hover:text-zinc-300 group-hover:opacity-100"
                title="Hide goroutines from this file"
              >
                <EyeOff className="h-3 w-3" />
              </button>
              {expandedGoroutines.has(gid) && (
                <div className="border-t border-zinc-700 bg-zinc-900/50 px-2 py-1.5">
                  <div className="mb-1 text-[10px] text-zinc-500">
                    State: <span className="text-zinc-400">{g.state}</span>
                    {g.wait_reason && (
                      <span className="ml-2">Wait: <span className="text-amber-400">{g.wait_reason}</span></span>
                    )}
                  </div>
                  <div className="max-h-32 overflow-y-auto overflow-x-hidden">
                    {g.stack.slice(0, 10).map((line, j) => (
                      <div key={j} className="break-all text-[9px] text-zinc-500">
                        {line}
                      </div>
                    ))}
                    {g.stack.length > 10 && (
                      <div className="text-[9px] text-zinc-600">
                        ... {g.stack.length - 10} more frames
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )})}
        
          {problemGoroutines.length === 0 && skippedGoroutines.size > 0 && (
            <div className="rounded bg-zinc-800/30 py-2 text-center text-[10px] text-zinc-500">
              All potential leaks hidden
            </div>
          )}
          {problemGoroutines.length === 0 && skippedGoroutines.size === 0 && (summary.long_waiting > 0 || summary.potentially_stuck > 0) && (
            <div className="rounded bg-zinc-800/30 px-2 py-2 text-center text-[10px] text-zinc-500">
              {summary.long_waiting > 0 && summary.potentially_stuck > 0
                ? `${summary.long_waiting} long-waiting and ${summary.potentially_stuck} stuck goroutines (background workers filtered)`
                : summary.long_waiting > 0
                  ? `${summary.long_waiting} long-waiting goroutines (background workers filtered)`
                  : `${summary.potentially_stuck} stuck goroutines (background workers filtered)`}
            </div>
          )}
        </div>
      )}

      {/* No problems message */}
      {problemGoroutines.length === 0 && summary.long_waiting === 0 && summary.potentially_stuck === 0 && (
        <div className="rounded bg-zinc-800/30 py-2 text-center text-[10px] text-zinc-500">
          No goroutine leaks detected
        </div>
      )}
    </div>
  )
}

export function DevProfiler (): React.ReactNode {
  const [isVisible, setIsVisible] = useState<boolean>(() => loadBooleanFromStorage(PROFILER_VISIBLE_KEY, true))
  const [isExpanded, setIsExpanded] = useState<boolean>(() => loadBooleanFromStorage(PROFILER_EXPANDED_KEY, true))
  const [isDismissed, setIsDismissed] = useState(false)
  const [expandedGoroutines, setExpandedGoroutines] = useState<Set<string>>(new Set())
  const [skippedGoroutines, setSkippedGoroutines] = useState<Set<string>>(() => loadSkippedGoroutineFiles())

  // Sync skipped goroutines to localStorage
  useEffect(() => {
    saveSkippedGoroutineFiles(skippedGoroutines)
  }, [skippedGoroutines])

  useEffect(() => {
    saveBooleanToStorage(PROFILER_VISIBLE_KEY, isVisible)
  }, [isVisible])

  useEffect(() => {
    saveBooleanToStorage(PROFILER_EXPANDED_KEY, isExpanded)
  }, [isExpanded])

  // Only fetch in development mode and when not dismissed
  const shouldFetch = isDevelopmentMode() && !isDismissed

  const { data, isLoading, error } = useGetDevPprofQuery(undefined, {
    pollingInterval: shouldFetch ? 10000 : 0, // Poll every 10 seconds
    skip: !shouldFetch,
  })

  const { data: goroutineData } = useGetDevGoroutinesQuery(undefined, {
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

  // Detect goroutine count trend (growing = potential leak)
  const goroutineTrend = useMemo(() => {
    if (!data?.history || data.history.length < 5 || !data?.runtime) return null
    const recent = data.history.slice(-5)
    const avg = recent.reduce((sum, p) => sum + p.goroutines, 0) / recent.length
    const current = data.runtime.num_goroutine
    const isGrowing = current > avg * 1.1 // 10% above average
    const growthPercent = avg > 0 ? ((current - avg) / avg) * 100 : 0
    return { isGrowing, growthPercent, avg }
  }, [data?.history, data?.runtime?.num_goroutine])

  // Filter problem goroutines (stuck or long-waiting, excluding expected background workers and skipped)
  const problemGoroutines = useMemo(() => {
    if (!goroutineData?.groups) return []
    return goroutineData.groups
      .filter((g) => {
        if (!g.wait_minutes || g.wait_minutes < 1) return false
        if (g.category === 'background') return false
        const filePath = getStackFilePath(g.stack)
        if (filePath && skippedGoroutines.has(filePath)) return false
        return true
      })
      .slice(0, 5)
  }, [goroutineData?.groups, skippedGoroutines])

  // Get goroutine health status
  const goroutineHealth = useMemo(() => {
    if (!goroutineData?.summary) return 'healthy'
    const { potentially_stuck, long_waiting } = goroutineData.summary
    if (potentially_stuck > 0) return 'critical'
    if (long_waiting > 0) return 'warning'
    return 'healthy'
  }, [goroutineData?.summary])

  const handleDismiss = useCallback(() => {
    setIsDismissed(true)
    saveBooleanToStorage(PROFILER_VISIBLE_KEY, false)
  }, [])

  const toggleGoroutineExpand = useCallback((id: string) => {
    setExpandedGoroutines((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }, [])

  const handleSkipGoroutine = useCallback((filePath: string) => {
    setSkippedGoroutines((prev) => {
      const next = new Set(prev)
      next.add(filePath)
      return next
    })
  }, [])

  const handleClearSkipped = useCallback(() => {
    setSkippedGoroutines(new Set())
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
        <div className="max-h-[70vh] overflow-y-auto overflow-x-hidden custom-scrollbar">
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
          <div className="border-b border-zinc-700 p-3">
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

          {/* Goroutine Health */}
          <GoroutineHealthSection
            goroutineData={goroutineData}
            goroutineHealth={goroutineHealth}
            goroutineTrend={goroutineTrend}
            problemGoroutines={problemGoroutines}
            expandedGoroutines={expandedGoroutines}
            toggleGoroutineExpand={toggleGoroutineExpand}
            skippedGoroutines={skippedGoroutines}
            onSkipGoroutine={handleSkipGoroutine}
            onClearSkipped={handleClearSkipped}
          />

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
