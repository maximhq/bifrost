"use client"

import FullPageLoader from "@/components/fullPageLoader"
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store"
import { useEffect } from "react"
import { toast } from "sonner"
import TeamsTable from "../views/teamsTable"

const POLLING_INTERVAL = 5000

export default function TeamsPage() {
  const { data: virtualKeysData, error: vkError, isLoading: vkLoading } = useGetVirtualKeysQuery(undefined, {
    pollingInterval: POLLING_INTERVAL,
  })
  const { data: teamsData, error: teamsError, isLoading: teamsLoading } = useGetTeamsQuery(
    {},
    { pollingInterval: POLLING_INTERVAL },
  )
  const { data: customersData, error: customersError, isLoading: customersLoading } = useGetCustomersQuery(undefined, {
    pollingInterval: POLLING_INTERVAL,
  })

  const isLoading = vkLoading || teamsLoading || customersLoading

  useEffect(() => {
    if (vkError) toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`)
    if (teamsError) toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`)
    if (customersError) toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`)
  }, [vkError, teamsError, customersError])

  if (isLoading) {
    return <FullPageLoader />
  }

  return (
    <div className="mx-auto w-full max-w-7xl">
      <TeamsTable
        teams={teamsData?.teams || []}
        customers={customersData?.customers || []}
        virtualKeys={virtualKeysData?.virtual_keys || []}
      />
    </div>
  )
}
