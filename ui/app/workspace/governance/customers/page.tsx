"use client"

import FullPageLoader from "@/components/fullPageLoader"
import { useDebouncedValue } from "@/hooks/useDebounce"
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import CustomersTable from "@/app/workspace/governance/views/customerTable"

const POLLING_INTERVAL = 5000
const PAGE_SIZE = 25

export default function GovernanceCustomersPage() {
  const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View)
  const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View)
  const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View)
  const shownErrorsRef = useRef(new Set<string>())

  const [search, setSearch] = useState("")
  const [offset, setOffset] = useState(0)
  const debouncedSearch = useDebouncedValue(search, 300)

  useEffect(() => {
    setOffset(0)
  }, [debouncedSearch])

  const { data: virtualKeysData, error: vkError, isLoading: vkLoading } = useGetVirtualKeysQuery(undefined, {
    skip: !hasVirtualKeysAccess,
    pollingInterval: POLLING_INTERVAL,
  })
  const { data: teamsData, error: teamsError, isLoading: teamsLoading } = useGetTeamsQuery(
    undefined,
    { skip: !hasTeamsAccess, pollingInterval: POLLING_INTERVAL },
  )
  const {
    data: customersData,
    error: customersError,
    isLoading: customersLoading,
  } = useGetCustomersQuery({
    limit: PAGE_SIZE,
    offset,
    search: debouncedSearch || undefined,
  }, {
    skip: !hasCustomersAccess,
    pollingInterval: POLLING_INTERVAL,
  })

  const customersTotal = customersData?.total_count ?? 0

  // Snap offset back when total shrinks past current page (e.g. delete last item on last page)
  useEffect(() => {
    if (!customersData || offset < customersTotal) return
    setOffset(customersTotal === 0 ? 0 : Math.floor((customersTotal - 1) / PAGE_SIZE) * PAGE_SIZE)
  }, [customersTotal, offset])

  const isLoading = vkLoading || teamsLoading || customersLoading

  useEffect(() => {
    if (!vkError && !teamsError && !customersError) {
      shownErrorsRef.current.clear()
      return
    }
    const errorKey = `${!!vkError}-${!!teamsError}-${!!customersError}`
    if (shownErrorsRef.current.has(errorKey)) return
    shownErrorsRef.current.add(errorKey)
    if (vkError && teamsError && customersError) {
      toast.error("Failed to load governance data.")
    } else {
      if (vkError) toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`)
      if (teamsError) toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`)
      if (customersError) toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`)
    }
  }, [vkError, teamsError, customersError])

  if (isLoading) {
    return <FullPageLoader />
  }

  return (
    <div className="mx-auto w-full max-w-7xl">
      <CustomersTable
        customers={customersData?.customers || []}
        totalCount={customersData?.total_count || 0}
        teams={teamsData?.teams || []}
        virtualKeys={virtualKeysData?.virtual_keys || []}
        search={search}
        debouncedSearch={debouncedSearch}
        onSearchChange={setSearch}
        offset={offset}
        limit={PAGE_SIZE}
        onOffsetChange={setOffset}
      />
    </div>
  )
}
