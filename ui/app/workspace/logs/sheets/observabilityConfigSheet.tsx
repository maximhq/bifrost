"use client"

import ObservabilityView from "@/app/workspace/config/views/observabilityView"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"

interface ObservabilityConfigSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ObservabilityConfigSheet({ open, onOpenChange }: ObservabilityConfigSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-4 overflow-x-hidden p-8 sm:max-w-[60%]"
      >
        <SheetHeader className="flex flex-row items-center px-0">
          <SheetTitle>Observability settings</SheetTitle>
        </SheetHeader>
        <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto px-6 py-2">
          <ObservabilityView />
        </div>
      </SheetContent>
    </Sheet>
  )
}
