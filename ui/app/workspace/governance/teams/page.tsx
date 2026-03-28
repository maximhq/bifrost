"use client"

import FullPageLoader from "@/components/fullPageLoader"
import { useDebouncedValue } from "@/hooks/useDebounce"
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { useEffect, useState } from "react"
import { toast } from "sonner"
import TeamsTable from "@/app/workspace/governance/views/teamsTable"

const POLLING_INTERVAL = 5000
const PAGE_SIZE = 25

export default function GovernanceTeamsPage() {
  const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View)
  const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View)
  const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View)

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
    {
      limit: PAGE_SIZE,
      offset,
      search: debouncedSearch || undefined,
    },
    { skip: !hasTeamsAccess, pollingInterval: POLLING_INTERVAL },
  )
  const { data: customersData, error: customersError, isLoading: customersLoading } = useGetCustomersQuery(undefined, {
    skip: !hasCustomersAccess,
    pollingInterval: POLLING_INTERVAL,
  })

  const teamsTotal = teamsData?.total_count ?? 0

  // Snap offset back when total shrinks past current page (e.g. delete last item on last page)
  useEffect(() => {
    if (!teamsData || offset < teamsTotal) return
    setOffset(teamsTotal === 0 ? 0 : Math.floor((teamsTotal - 1) / PAGE_SIZE) * PAGE_SIZE)
  }, [teamsTotal, offset])

  const isLoading = vkLoading || teamsLoading || customersLoading

  useEffect(() => {
    if (vkError) toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`)
    if (teamsError) toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`)
    if (customersError) toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`)
  }, [vkError, teamsError, customersError])

  if (isLoading) {
    return <FullPageLoader />
  }

  const teams = teamsData?.teams || []
  const totalCount = teamsData?.total_count || 0

  return (
    <div className="mx-auto w-full max-w-7xl">
      <TeamsTable
        teams={teams}
        totalCount={totalCount}
        customers={customersData?.customers || []}
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
