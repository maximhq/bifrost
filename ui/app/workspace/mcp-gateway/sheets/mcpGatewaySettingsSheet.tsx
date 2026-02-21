"use client"

import MCPView from "@/app/workspace/config/views/mcpView"
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet"

interface MCPGatewaySettingsSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function MCPGatewaySettingsSheet({ open, onOpenChange }: MCPGatewaySettingsSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-3xl"
      >
        <div className="custom-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto pt-8">
          <MCPView />
        </div>
      </SheetContent>
    </Sheet>
  )
}
