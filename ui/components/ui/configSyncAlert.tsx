import { Info } from "lucide-react"

import { Alert, AlertDescription } from "@/components/ui/alert"

interface ConfigSyncAlertProps {
  className?: string
}

export function ConfigSyncAlert({ className }: ConfigSyncAlertProps) {
  return (
    <Alert variant="info" className={className}>
      <Info className="h-4 w-4" />
      <AlertDescription>
        <p>This config is synced from <code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">config.json</code>.
        Any future updates to config.json will overwrite UI changes. If you are using config.json to bootstrap the initial config, you can ignore this alert.</p>
      </AlertDescription>
    </Alert>
  )
}
