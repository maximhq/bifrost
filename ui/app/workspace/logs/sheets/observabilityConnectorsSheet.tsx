"use client"

import ObservabilityView from "@/app/workspace/config/views/observabilityView"
import ObservabilityConnectorsView from "@/app/workspace/observability/views/observabilityView"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet"
import { ChevronLeft } from "lucide-react"
import { useState } from "react"

type SheetView = "connectors" | "settings"

interface ObservabilityConnectorsSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ObservabilityConnectorsSheet({ open, onOpenChange }: ObservabilityConnectorsSheetProps) {
  const [view, setView] = useState<SheetView>("connectors")

  const handleOpenChange = (next: boolean) => {
    if (!next) setView("connectors")
    onOpenChange(next)
  }

  const showSettings = () => setView("settings")
  const showConnectors = () => setView("connectors")

  return (
    <Sheet open={open} onOpenChange={handleOpenChange}>
      <SheetContent
        side="right"
        className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-3xl"
      >
        <SheetTitle className="sr-only">{view === "connectors" ? "Connectors" : "Observability settings"}</SheetTitle>
        <div className="custom-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto pt-8">
          {view === "connectors" ? (
            <>
              <h2 className="text-lg font-semibold mb-4">Connectors</h2>
              <ObservabilityConnectorsView onOpenObservabilityConfig={showSettings} />
            </>
          ) : (
            <>
              <div className="mb-0 flex items-center gap-2">
                <Button variant="ghost" size="icon" className="-ml-2 h-8 w-8" onClick={showConnectors} aria-label="Back to connectors">
                  <ChevronLeft className="size-4" />
                </Button>
                <h2 className="text-lg font-semibold">Observability settings</h2>
              </div>
              <ObservabilityView />
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
