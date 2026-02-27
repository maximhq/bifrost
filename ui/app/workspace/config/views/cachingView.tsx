"use client";

import { getErrorMessage, useGetCoreConfigQuery } from "@/lib/store";
import PluginsForm from "./pluginsForm";

export default function CachingView() {
	const { data: bifrostConfig, isLoading, error: configError } = useGetCoreConfigQuery({ fromDB: true });

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Caching</h2>
				<p className="text-muted-foreground text-sm">Configure semantic caching for requests.</p>
			</div>

			{isLoading && (
				<div className="flex items-center justify-center py-8">
					<p className="text-muted-foreground">Loading configuration...</p>
				</div>
			)}

			{configError !== undefined && (
				<div className="border-destructive/50 bg-destructive/10 rounded-lg border p-4">
					<p className="text-destructive text-sm font-medium">Failed to load configuration</p>
					<p className="text-muted-foreground mt-1 text-sm">
						{getErrorMessage(configError) || "An unexpected error occurred. Please try again."}
					</p>
				</div>
			)}

			{!isLoading && !configError && <PluginsForm isVectorStoreEnabled={bifrostConfig?.is_cache_connected ?? false} />}
		</div>
	);
}
