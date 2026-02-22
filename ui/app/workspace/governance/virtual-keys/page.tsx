"use client"

import FullPageLoader from "@/components/fullPageLoader"
import {
	getErrorMessage,
	useGetCustomersQuery,
	useGetTeamsQuery,
	useGetVirtualKeysQuery,
} from "@/lib/store"
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib"
import { useEffect, useRef } from "react"
import { toast } from "sonner"
import VirtualKeysTable from "@/app/workspace/virtual-keys/views/virtualKeysTable"

const POLLING_INTERVAL = 5000

export default function GovernanceVirtualKeysPage() {
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View)
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View)
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View)
	const shownErrorsRef = useRef(new Set<string>())

	const {
		data: virtualKeysData,
		error: vkError,
		isLoading: vkLoading,
	} = useGetVirtualKeysQuery(undefined, {
		skip: !hasVirtualKeysAccess,
		pollingInterval: POLLING_INTERVAL,
	})

	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
	} = useGetTeamsQuery({}, {
		skip: !hasTeamsAccess,
		pollingInterval: POLLING_INTERVAL,
	})

	const {
		data: customersData,
		error: customersError,
		isLoading: customersLoading,
	} = useGetCustomersQuery(undefined, {
		skip: !hasCustomersAccess,
		pollingInterval: POLLING_INTERVAL,
	})

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
			<VirtualKeysTable
				virtualKeys={virtualKeysData?.virtual_keys || []}
				teams={teamsData?.teams || []}
				customers={customersData?.customers || []}
			/>
		</div>
	)
}
