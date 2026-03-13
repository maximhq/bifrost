"use client"

import { GripVerticalIcon } from "lucide-react"
import type { GroupProps, PanelProps, SeparatorProps } from "react-resizable-panels"
import { Group, Panel, Separator } from "react-resizable-panels"

import { cn } from "@/lib/utils"

// Map 'direction' prop to 'orientation' for backward compatibility
type ResizablePanelGroupProps = Omit<GroupProps, 'orientation'> & {
  direction?: 'horizontal' | 'vertical'
}

function ResizablePanelGroup({
  className,
  direction = 'horizontal',
  ...props
}: ResizablePanelGroupProps) {
  return (
    <Group
      data-slot="resizable-panel-group"
      data-panel-group-direction={direction}
      orientation={direction}
      className={cn(
        "flex h-full w-full data-[panel-group-direction=vertical]:flex-col",
        className
      )}
      {...props}
    />
  )
}

function ResizablePanel({
  className,
  ...props
}: PanelProps & { className?: string }) {
  return <Panel data-slot="resizable-panel" className={className} {...props} />
}

function ResizableHandle({
  hideHandle,
  className,
  ...props
}: SeparatorProps & {
  hideHandle?: boolean
}) {
  return (
    <Separator
      data-slot="resizable-handle"
      className={cn(
        "bg-border focus-visible:ring-ring group/resizable-handle relative flex w-px items-center justify-center after:absolute after:inset-y-0 after:left-1/2 after:w-1 after:-translate-x-1/2 focus-visible:ring-1 focus-visible:ring-offset-1 focus-visible:outline-hidden data-[panel-group-direction=vertical]:h-px data-[panel-group-direction=vertical]:w-full data-[panel-group-direction=vertical]:after:left-0 data-[panel-group-direction=vertical]:after:h-1 data-[panel-group-direction=vertical]:after:w-full data-[panel-group-direction=vertical]:after:translate-x-0 data-[panel-group-direction=vertical]:after:-translate-y-1/2 [&[data-panel-group-direction=vertical]>div]:rotate-90",
        className
      )}
      {...props}
    >
      {!hideHandle && (
        <div className="bg-border z-10 flex h-5 w-3 items-center justify-center rounded-sm border opacity-0 transition-opacity group-hover/resizable-handle:opacity-100 group-active/resizable-handle:opacity-100">
          <GripVerticalIcon className="size-2.5" />
        </div>
      )}
    </Separator>
  )
}

export { ResizableHandle, ResizablePanel, ResizablePanelGroup }

