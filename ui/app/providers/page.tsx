"use client";

import ConfiguredKeys from "@/app/providers/views/providerList";
import FullPageLoader from "@/components/full-page-loader";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useGetProvidersQuery } from "@/lib/store";
import { useEffect } from "react";

export default function Providers() {
	const { data, error, isLoading } = useGetProvidersQuery();

	const { toast } = useToast();

	useEffect(() => {
		if (error) {
			toast({
				title: "Error",
				description: getErrorMessage(error),
				variant: "destructive",
			});
		}
	}, [error, toast]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div>
			<ConfiguredKeys providers={data?.providers || []} />
		</div>
	);
}
