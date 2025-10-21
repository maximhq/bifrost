"use client";

import { Button } from "@/components/ui/button";
import { useGetPluginsQuery } from "@/lib/store";
import { Plugin } from "@/lib/types/plugins";
import { PlusIcon } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import AddNewPluginSheet from "./sheets/addNewPluginSheet";

export default function PluginsPage() {
	const { data: plugins, isLoading } = useGetPluginsQuery();
	const customPlugins = useMemo(() => plugins?.filter((plugin) => plugin.isCustom), [plugins]);
	const [isSheetOpen, setIsSheetOpen] = useState(false);
	const [selectedPlugin, setSelectedPlugin] = useState<Plugin | null>(null);

	const handleAddNew = () => {
		setSelectedPlugin(null);
		setIsSheetOpen(true);
	};

	const handleCloseSheet = () => {
		setIsSheetOpen(false);
		setSelectedPlugin(null);
	};

	return (
		<div className="mx-auto w-full max-w-7xl">
			<div className="flex w-[250px] flex-col gap-2 pb-10">
				<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
					<div className="mb-4">
						{customPlugins?.map((plugin) => (
							<div key={plugin.name}>{plugin.name}</div>
						))}
						<div className="my-4">
							<Button
								variant="outline"
								size="sm"
								className="w-full justify-start"
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
						<div className="text-sm">
							Read our{" "}
							<Link className="text-primary hover:underline dark:text-green-400" href="https://docs.getbifrost.ai/plugins" target="_blank">
								documentation
							</Link>{" "}
							to learn more about plugins.
						</div>
					</div>
				</div>
			</div>

			<AddNewPluginSheet 
				open={isSheetOpen} 
				onClose={handleCloseSheet} 
				plugin={selectedPlugin}
			/>
		</div>
	);
}
