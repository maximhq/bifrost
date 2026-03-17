import { LargePayloadConfig } from "@enterprise/lib/types/largePayload"

export const useGetLargePayloadConfigQuery = (): {
  data: LargePayloadConfig | undefined
  isLoading: boolean
  isError: boolean
  error: null
} => ({
  data: undefined,
  isLoading: false,
  isError: false,
  error: null,
})

export const useUpdateLargePayloadConfigMutation = (): [
  (_config: LargePayloadConfig) => { unwrap: () => Promise<void> },
  { isLoading: boolean }
] => [
  () => ({ unwrap: async () => {} }),
  { isLoading: false },
]
