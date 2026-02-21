"use client"

import { NoPermissionView } from "@/components/noPermissionView"
import { IS_ENTERPRISE } from "@/lib/constants/config"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"

export default function GuardrailsLayout({ children }: { children: React.ReactNode }) {
  const hasGuardrailsConfigAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View)
  const hasGuardrailsProvidersAccess = useRbac(RbacResource.GuardrailsProviders, RbacOperation.View)
  const hasGuardrailsAccess = hasGuardrailsConfigAccess || hasGuardrailsProvidersAccess
  // OSS: single tab shows enterprise paywall; enterprise: require access for config or providers
  if (IS_ENTERPRISE && !hasGuardrailsAccess) {
    return <NoPermissionView entity="guardrails configuration" />
  }
  return <div>{children}</div>
}
