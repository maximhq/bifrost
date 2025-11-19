import { baseApi } from './baseApi'

// Memory statistics at a point in time
export interface MemoryStats {
  alloc: number
  total_alloc: number
  heap_inuse: number
  heap_objects: number
  sys: number
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

// Allocation info for top allocations
export interface AllocationInfo {
  function: string
  file: string
  line: number
  bytes: number
  count: number
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

export const devApi = baseApi.injectEndpoints({
  endpoints: (builder) => ({
    // Get dev pprof data - polls every 10 seconds
    getDevPprof: builder.query<PprofData, void>({
      query: () => ({
        url: '/dev/pprof',
      }),
    }),
  }),
})

export const {
  useGetDevPprofQuery,
  useLazyGetDevPprofQuery,
} = devApi

