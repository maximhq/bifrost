"use client"

import { MCPGatewaySettingsSheet } from "@/app/workspace/mcp-gateway/sheets/mcpGatewaySettingsSheet"
import { Button } from "@/components/ui/button"
import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { Cog, Server, Settings, ShieldUser, ToolCase } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useEffect, useRef, useState } from "react"
import { cn } from "@/lib/utils"

const tabs = [
  { label: "MCP servers", href: "/workspace/mcp-gateway/servers", icon: Server },
  { label: "Tool groups", href: "/workspace/mcp-gateway/tool-groups", icon: ToolCase },
  { label: "Auth config", href: "/workspace/mcp-gateway/auth-config", icon: ShieldUser },
]

export default function MCPGatewayLayout({ children }: { children: React.ReactNode }) {
  const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View)
  const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View)
  const pathname = usePathname()
  const headerRef = useRef<HTMLDivElement>(null)
  const tabRefs = useRef<(HTMLAnchorElement | null)[]>([])
  const [indicatorStyle, setIndicatorStyle] = useState({ left: 0, width: 0 })
  const [settingsSheetOpen, setSettingsSheetOpen] = useState(false)

  const path = pathname.replace(/\/$/, "") || "/"
  const activeIndex = tabs.findIndex((tab) => path === tab.href || path.startsWith(tab.href + "/"))

  useEffect(() => {
    const header = headerRef.current
    const el = activeIndex >= 0 ? tabRefs.current[activeIndex] : null
    if (header && el) {
      const headerRect = header.getBoundingClientRect()
      const tabRect = el.getBoundingClientRect()
      setIndicatorStyle({
        left: tabRect.left - headerRect.left,
        width: tabRect.width,
      })
    }
  }, [activeIndex, pathname])

  if (!hasMCPGatewayAccess) {
    return <NoPermissionView entity="MCP gateway configuration" />
  }

  return (
    <div className="flex flex-col h-full">
      <div ref={headerRef} className="relative mb-7 w-full border-b border-border">
        <div className="flex w-full h-10 items-center justify-between gap-2 pb-3">
          <div className="relative flex h-full items-center gap-1">
            {tabs.map((tab, i) => {
              const isActive = i === activeIndex
              return (
                <Link
                  key={tab.href}
                  ref={(el) => { tabRefs.current[i] = el }}
                  href={tab.href}
                  className={cn(
                    "inline-flex cursor-pointer items-center justify-center gap-1.5 px-5 py-3.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
                    isActive ? "text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  <tab.icon className="size-4" />
                  {tab.label}
                </Link>
              )
            })}
          </div>
          {hasSettingsAccess && (
            <Button variant="outline" size="sm" onClick={() => setSettingsSheetOpen(true)}>
            <Settings className="size-4" />
              Settings
            </Button>
          )}
        </div>
        <span
          className="absolute bottom-0 left-0 h-0.5 bg-primary transition-[transform,width] duration-200 ease-out will-change-transform"
          style={{ width: indicatorStyle.width, transform: `translateX(${indicatorStyle.left}px)` }}
          aria-hidden
        />
      </div>
      <div className="min-h-0 flex-1">{children}</div>
      {hasSettingsAccess && (
        <MCPGatewaySettingsSheet open={settingsSheetOpen} onOpenChange={setSettingsSheetOpen} />
      )}
    </div>
  )
}
