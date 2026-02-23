"use client"

import { Button } from "@/components/ui/button"
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store"
import { cn } from "@/lib/utils"
import { Landmark } from "lucide-react"
import { useCallback } from "react"
import { toast } from "sonner"

export function GovernanceDisabledView() {
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
        client_config: { ...bifrostConfig.client_config, enable_governance: true },
      }).unwrap()
      toast.success("Governance enabled.")
    } catch (error) {
      toast.error(getErrorMessage(error))
    }
  }, [bifrostConfig, updateCoreConfig])

  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-4 text-center mx-auto w-full max-w-7xl min-h-[80vh]"
      )}
    >
      <div className="text-muted-foreground">
        <Landmark className="h-10 w-10" />
      </div>
      <div className="flex flex-col gap-1">
        <h1 className="text-muted-foreground text-xl font-medium">Governance is disabled</h1>
        <div className="text-muted-foreground mt-2 max-w-[600px] text-sm font-normal">
          Enable governance to manage virtual keys, teams, customers, and access control.
        </div>
      </div>
      <Button onClick={handleEnable} disabled={isLoading}>
        {isLoading ? "Enabling…" : "Enable governance"}
      </Button>
    </div>
  )
}
