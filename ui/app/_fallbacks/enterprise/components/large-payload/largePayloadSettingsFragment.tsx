import { LargePayloadConfig } from "@enterprise/lib/types/largePayload"

export interface LargePayloadSettingsFragmentProps {
  config: LargePayloadConfig
  onConfigChange: (config: LargePayloadConfig) => void
  controlsDisabled: boolean
}

export default function LargePayloadSettingsFragment(_props: LargePayloadSettingsFragmentProps) {
  return null
}
