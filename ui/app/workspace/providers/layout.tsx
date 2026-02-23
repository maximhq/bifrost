"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { Box, Calculator, CircleDollarSign, Route, Wallet } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useEffect, useRef, useState } from "react"
import { cn } from "@/lib/utils"

const tabs = [
  { label: "Model providers", href: "/workspace/providers", icon: Box },
  { label: "Budgets & limits", href: "/workspace/providers/model-limits", icon: Wallet },
  { label: "Routing rules", href: "/workspace/providers/routing-rules", icon: Route },
  { label: "Pricing overrides", href: "/workspace/providers/pricing-overrides", icon: Calculator },
  { label: "Custom pricing", href: "/workspace/providers/custom-pricing", icon: CircleDollarSign },
]

export default function ProvidersLayout({ children }: { children: React.ReactNode }) {
  const hasProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View)
  const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.View)
  const hasRoutingRulesAccess = useRbac(RbacResource.RoutingRules, RbacOperation.View)
  const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View)
  const pathname = usePathname()
  const headerRef = useRef<HTMLDivElement>(null)
  const tabRefs = useRef<(HTMLAnchorElement | null)[]>([])
  const [indicatorStyle, setIndicatorStyle] = useState({ left: 0, width: 0 })

  const visibleTabs = tabs.filter(
    (tab) =>
      (tab.href === "/workspace/providers" && hasProvidersAccess) ||
      (tab.href === "/workspace/providers/custom-pricing" && hasSettingsAccess) ||
      (tab.href === "/workspace/providers/routing-rules" && hasRoutingRulesAccess) ||
      (tab.href === "/workspace/providers/pricing-overrides" && hasGovernanceAccess) ||
      (tab.href === "/workspace/providers/model-limits" && hasGovernanceAccess),
  )

  const path = pathname.replace(/\/$/, "") || "/"
  const activeIndex = visibleTabs.findIndex((tab) =>
    tab.href === "/workspace/providers"
      ? path === "/workspace/providers" || path === "/workspace/providers/"
      : path === tab.href || path.startsWith(tab.href + "/"),
  )

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

  if (!hasProvidersAccess && !hasSettingsAccess && !hasRoutingRulesAccess && !hasGovernanceAccess) {
    return <NoPermissionView entity="models" />
  }

  return (
    <div className="flex flex-col h-full">
      <div ref={headerRef} className="relative mb-7 w-full border-b border-border">
        <div className="flex w-full h-10 items-center gap-2 pb-3">
          <div className="relative flex h-full items-center gap-1">
            {visibleTabs.map((tab, i) => {
              const isActive = i === activeIndex
              return (
                <Link
                  key={tab.href}
                  ref={(el) => { tabRefs.current[i] = el }}
                  href={tab.href}
                  className={cn(
                    "inline-flex cursor-pointer items-center justify-center gap-1.5 px-5 py-2.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
                    isActive ? "text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  <tab.icon className="size-4" />
                  {tab.label}
                </Link>
              )
            })}
          </div>
        </div>
        <span
          className="absolute bottom-0 left-0 h-0.5 bg-primary transition-[transform,width] duration-200 ease-out will-change-transform"
          style={{ width: indicatorStyle.width, transform: `translateX(${indicatorStyle.left}px)` }}
          aria-hidden
        />
      </div>
      <div className="min-h-0 flex-1">{children}</div>
    </div>
  )
}
