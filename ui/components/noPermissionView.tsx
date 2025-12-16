"use client"

import { cn } from "@/lib/utils"
import { ShieldX } from "lucide-react"

interface NoPermissionViewProps {
  entity: string
  className?: string
  align?: "middle" | "top"
}

export function NoPermissionView({ entity, className, align = "middle" }: NoPermissionViewProps) {
  return (
    <div className={cn("flex min-h-[calc(100vh-200px)] flex-col items-center  gap-4 text-center", align === "middle" ? "justify-center" : "justify-start", className)}>
      <div className="text-muted-foreground">
        <ShieldX className="h-16 w-16" strokeWidth={1} />
      </div>
      <div className="flex flex-col gap-1 items-center">
        <h1 className="text-muted-foreground text-xl font-medium">
          You don't have permission to view {entity}
        </h1>
        <p className="text-muted-foreground mt-2 max-w-[400px] text-sm font-normal">
          Contact your administrator to request access to this resource.
        </p>
      </div>
    </div>
  )
}
