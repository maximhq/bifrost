"use client"

import FullPageLoader from "@/components/fullPageLoader"
import { NoPermissionView } from "@/components/noPermissionView"
import { useGetCoreConfigQuery } from "@/lib/store"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { Building, KeyRound, UserRoundCheck, Users, WalletCards } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useEffect, useMemo, useRef, useState } from "react"
import { cn } from "@/lib/utils"

const allTabs = [
  { label: "Virtual Keys", href: "/workspace/governance/virtual-keys", icon: KeyRound, testId: "governance-tab-virtual-keys" },
  { label: "Users", href: "/workspace/governance/users", icon: Users, testId: "governance-tab-users" },
  { label: "Teams", href: "/workspace/governance/teams", icon: Building, testId: "governance-tab-teams" },
  { label: "Customers", href: "/workspace/governance/customers", icon: WalletCards, testId: "governance-tab-customers" },
  { label: "Roles & Permissions", href: "/workspace/governance/rbac", icon: UserRoundCheck, testId: "governance-tab-rbac" },
]

export default function GovernanceLayout({ children }: { children: React.ReactNode }) {
  const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View)
  const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View)
  const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View)
  const hasRbacAccess = useRbac(RbacResource.RBAC, RbacOperation.View)
  const { isLoading } = useGetCoreConfigQuery({ fromDB: true })
  const pathname = usePathname()
  const headerRef = useRef<HTMLDivElement>(null)
  const tabRefs = useRef<(HTMLAnchorElement | null)[]>([])
  const [indicatorStyle, setIndicatorStyle] = useState({ left: 0, width: 0 })

  const tabs = useMemo(() => {
    return allTabs.filter((tab) => {
      if (tab.href === "/workspace/governance/virtual-keys") return hasVirtualKeysAccess
      if (tab.href === "/workspace/governance/users") return true
      if (tab.href === "/workspace/governance/teams") return hasTeamsAccess
      if (tab.href === "/workspace/governance/customers") return hasCustomersAccess
      if (tab.href === "/workspace/governance/rbac") return hasRbacAccess
      return true
    })
  }, [hasVirtualKeysAccess, hasTeamsAccess, hasCustomersAccess, hasRbacAccess])

  const path = pathname.replace(/\/$/, "") || "/"
  const activeIndex = (() => {
    const i = tabs.findIndex((tab) => path === tab.href || path.startsWith(tab.href + "/"))
    if (i >= 0) return i
    if (path === "/workspace/governance") return 0
    return 0
  })()

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

  if (isLoading) {
    return <FullPageLoader />
  }

  // Users tab is always visible, so only block access when no tabs would be shown
  // (Currently Users is unconditionally included, so this guard is always false)
  if (!hasVirtualKeysAccess && !hasCustomersAccess && !hasTeamsAccess && !hasRbacAccess && tabs.length === 0) {
    return <NoPermissionView entity="users and groups" />
  }

  return (
    <div className="flex flex-col h-full">
      <div ref={headerRef} className="relative mb-7 w-full border-b border-border">
        <div className="flex w-full h-10 items-center gap-2 pb-3">
          <div className="relative flex h-full items-center gap-1">
            {tabs.map((tab, i) => {
              const isActive = i === activeIndex
              return (
                <Link
                  key={tab.href}
                  ref={(el) => { tabRefs.current[i] = el }}
                  href={tab.href}
                  data-testid={tab.testId}
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
