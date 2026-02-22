"use client"

import { Button } from "@/components/ui/button"
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store"
import { cn } from "@/lib/utils"
import { ScrollText } from "lucide-react"
import { useCallback } from "react"
import { toast } from "sonner"

export function LoggingDisabledView() {
  const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true })
  const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation()

  const handleEnable = useCallback(async () => {
    if (!bifrostConfig?.client_config) {
      toast.error("Configuration not loaded")
      return
    }
    try {
      await updateCoreConfig({
        ...bifrostConfig,
        client_config: { ...bifrostConfig.client_config, enable_logging: true },
      }).unwrap()
      toast.success("Logging enabled.")
    } catch (error) {
      toast.error(getErrorMessage(error))
    }
  }, [bifrostConfig, updateCoreConfig])

  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-4 text-center mx-auto w-full max-w-7xl min-h-[calc(100vh-130px)]"
      )}
    >
      <div className="text-muted-foreground">
        <ScrollText className="h-10 w-10" />
      </div>
      <div className="flex flex-col gap-1">
        <h1 className="text-muted-foreground text-xl font-medium">Logging is disabled</h1>
        <div className="text-muted-foreground mt-2 max-w-[600px] text-sm font-normal">
          Enable logging to view LLM and MCP request logs, traces, and observability data.
        </div>
      </div>
      <Button onClick={handleEnable} disabled={isLoading}>
        {isLoading ? "Enabling…" : "Enable logging"}
      </Button>
    </div>
  )
}
