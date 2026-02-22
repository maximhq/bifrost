"use client";

import { Button } from "@/components/ui/button";
import { setSelectedPlugin, useAppDispatch, useAppSelector, useGetPluginsQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { PlusIcon, Puzzle } from "lucide-react";
import Link from "next/link";
import { useQueryState } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import AddNewPluginSheet from "./sheets/addNewPluginSheet";
import { PluginsEmptyState } from "./views/pluginsEmptyState";
import PluginsView from "./views/pluginsView";

export default function PluginsPage() {
	const dispatch = useAppDispatch();
	const hasCreatePluginAccess = useRbac(RbacResource.Plugins, RbacOperation.Create);
	const { data: plugins, isLoading } = useGetPluginsQuery();
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const [selectedPluginId, setSelectedPluginId] = useQueryState("plugin");
	const customPlugins = useMemo(() => plugins?.filter((plugin) => plugin.isCustom), [plugins]);
	const [isSheetOpen, setIsSheetOpen] = useState(false);

	const handleAddNew = () => {
		setIsSheetOpen(true);
	};

	const handleCloseSheet = () => {
		setIsSheetOpen(false);
	};

	useEffect(() => {
		if (!selectedPluginId) return;
		const plugin = customPlugins?.find((plugin) => plugin.name === selectedPluginId);
		if (plugin) {
			dispatch(setSelectedPlugin(plugin));
		}
	}, [selectedPluginId, customPlugins]);

	useEffect(() => {
		if (selectedPluginId) return;
		if (!selectedPlugin) {
			setSelectedPluginId(customPlugins?.[0]?.name ?? "");
			return;
		}
		setSelectedPluginId(selectedPlugin?.name ?? "");
	}, [customPlugins]);

	if (customPlugins?.length === 0 && !isLoading) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<PluginsEmptyState onCreateClick={handleAddNew} canCreate={hasCreatePluginAccess} />
				<AddNewPluginSheet
					open={isSheetOpen}
					onClose={handleCloseSheet}
					onCreate={(pluginName) => {
						setSelectedPluginId(pluginName);
					}}
				/>
			</div>
		);
	}

	return (
		<div className="mx-auto w-full max-w-7xl">
			<div className="flex flex-row gap-4">
				<div className="flex min-w-[250px] flex-col gap-2 pb-10">
					<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
						<div className="mb-4">
							<div className="text-muted-foreground mb-2 text-xs font-medium">Plugins</div>
							{customPlugins?.map((plugin) => (
								<button
									type="button"
									key={plugin.name}
									aria-current={selectedPlugin?.name === plugin.name ? "page" : undefined}
									className={cn(
										"mb-1 flex max-h-[32px] w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
										selectedPlugin?.name === plugin.name
											? "bg-secondary opacity-100 hover:opacity-100"
											: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
									)}
									onClick={() => {
										setSelectedPluginId(plugin.name);
									}}
								>
									<div className="flex flex-row items-center gap-2">
										<div className="w-[16px]">
											<Puzzle className="text-muted-foreground size-3.5" />
										</div>{" "}
										<span className="">{plugin.name}</span>
									</div>
									<div
										className={cn(
											"ml-auto h-2 w-2 animate-pulse rounded-full",
											plugin.status?.status === "active" ? "bg-green-800 dark:bg-green-200" : "bg-red-800 dark:bg-red-400",
										)}
									/>
								</button>
							))}
							<div className="my-4">
								<Button
									variant="outline"
									size="sm"
									className="w-full justify-start"
									disabled={!hasCreatePluginAccess}
									onClick={(e) => {
										e.preventDefault();
										e.stopPropagation();
										handleAddNew();
									}}
								>
									<PlusIcon className="h-4 w-4" />
									<div className="text-xs">Install New Plugin</div>
								</Button>
							</div>
						</div>
					</div>
				</div>
				<PluginsView
					onDelete={() => {
						setSelectedPluginId(customPlugins?.[0]?.name ?? "");
					}}
					onCreate={(pluginName) => {
						setSelectedPluginId(pluginName ?? "");
					}}
				/>
			</div>
			<AddNewPluginSheet
			open={isSheetOpen}
			onClose={handleCloseSheet}
			onCreate={(pluginName) => {
				setSelectedPluginId(pluginName);
			}}
		/>
		</div>
	);
}
