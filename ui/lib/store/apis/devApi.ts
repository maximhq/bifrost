import { baseApi } from './baseApi'

// Memory statistics at a point in time
export interface MemoryStats {
  alloc: number
  total_alloc: number
  heap_inuse: number
  heap_objects: number
  sys: number
  // Detailed breakdown
  heap_idle: number
  heap_released: number
  heap_sys: number
  stack_inuse: number
  stack_sys: number
  mspan_inuse: number
  mcache_inuse: number
  buck_hash_sys: number
  gc_sys: number
  other_sys: number
}

// CPU statistics
export interface CPUStats {
  usage_percent: number
  user_time: number
  system_time: number
}

// Runtime statistics
export interface RuntimeStats {
  num_goroutine: number
  num_gc: number
  gc_pause_ns: number
  num_cpu: number
  gomaxprocs: number
}

// Single frame in a call stack
export interface StackFrame {
  function: string
  file: string
  line: number
}

// Allocation info for top allocations with full call stack
export interface AllocationInfo {
  function: string
  file: string
  line: number
  bytes: number
  count: number
  stack: StackFrame[]
}

// Single point in the metrics history
export interface HistoryPoint {
  timestamp: string
  alloc: number
  heap_inuse: number
  goroutines: number
  gc_pause_ns: number
  cpu_percent: number
}

// Complete pprof data response
export interface PprofData {
  timestamp: string
  memory: MemoryStats
  cpu: CPUStats
  runtime: RuntimeStats
  top_allocations: AllocationInfo[]
  history: HistoryPoint[]
}

// Goroutine group representing goroutines with same stack trace
export interface GoroutineGroup {
  count: number
  state: string
  wait_reason?: string
  wait_minutes?: number
  top_func: string
  stack: string[]
  category: 'background' | 'per-request' | 'unknown'
}

// Goroutine health summary
export interface GoroutineSummary {
  background: number
  per_request: number
  long_waiting: number
  potentially_stuck: number
}

// Goroutine profile response
export interface GoroutineProfile {
  timestamp: string
  total_goroutines: number
  groups: GoroutineGroup[]
  summary: GoroutineSummary
}

export const devApi = baseApi.injectEndpoints({
  endpoints: (builder) => ({
    // Get dev pprof data - polls every 10 seconds
    getDevPprof: builder.query<PprofData, void>({
      query: () => ({
        url: '/dev/pprof',
      }),
    }),
    // Get goroutine profile for leak detection
    getDevGoroutines: builder.query<GoroutineProfile, void>({
      query: () => ({
        url: '/dev/pprof/goroutines',
      }),
    }),
  }),
})

export const {
  useGetDevPprofQuery,
  useLazyGetDevPprofQuery,
  useGetDevGoroutinesQuery,
  useLazyGetDevGoroutinesQuery,
} = devApi

