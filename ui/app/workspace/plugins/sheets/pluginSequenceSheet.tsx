"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage, useUpdatePluginMutation } from "@/lib/store";
import { Plugin } from "@/lib/types/plugins";
import { cn } from "@/lib/utils";
import { DragDropProvider } from "@dnd-kit/react";
import { useSortable } from "@dnd-kit/react/sortable";
import { GripVertical, Lock } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";

const BUILTIN_ID = "__builtin__";

interface SequenceItem {
	id: string;
	type: "builtin" | "custom";
	plugin?: Plugin;
}

interface PluginSequenceSheetProps {
	open: boolean;
	onClose: () => void;
	plugins: Plugin[];
}

function buildSequenceItems(plugins: Plugin[]): SequenceItem[] {
	const customPlugins = plugins.filter((p) => p.isCustom);

	const preBuiltin = customPlugins.filter((p) => p.placement === "pre_builtin").sort((a, b) => (a.order ?? 0) - (b.order ?? 0));

	const postBuiltin = customPlugins.filter((p) => p.placement !== "pre_builtin").sort((a, b) => (a.order ?? 0) - (b.order ?? 0));

	return [
		...preBuiltin.map((p) => ({ id: p.name, type: "custom" as const, plugin: p })),
		{ id: BUILTIN_ID, type: "builtin" as const },
		...postBuiltin.map((p) => ({ id: p.name, type: "custom" as const, plugin: p })),
	];
}

function SortableBlock({ item, index }: { item: SequenceItem; index: number }) {
	const isBuiltin = item.type === "builtin";
	const { ref, isDragging, handleRef, sourceRef, targetRef } = useSortable({
		id: item.id,
		index,
	});

	return (
		<div
			ref={isBuiltin ? targetRef : ref}
			className={cn(
				"flex items-center gap-3 rounded-md border px-3 py-2.5 transition-colors",
				isBuiltin ? "border-dashed bg-zinc-100 dark:bg-zinc-800/50" : "bg-white dark:bg-zinc-900",
				isDragging && "opacity-50",
			)}
		>
			{isBuiltin ? (
				<Lock className="text-muted-foreground h-4 w-4 shrink-0" />
			) : (
				<div ref={handleRef} className="cursor-grab active:cursor-grabbing" data-testid={`plugin-sequence-handle-${item.id}`}>
					<GripVertical className="text-muted-foreground h-4 w-4 shrink-0" />
				</div>
			)}
			<span className={cn("text-sm", isBuiltin && "text-muted-foreground font-medium")}>
				{isBuiltin ? "Built-in Plugins" : item.plugin?.name}
			</span>
			{!isBuiltin && item.plugin?.status && (
				<div
					className={cn(
						"ml-auto h-2 w-2 animate-pulse rounded-full",
						item.plugin.status.status === "active" ? "bg-green-800 dark:bg-green-200" : "bg-red-800 dark:bg-red-400",
					)}
				/>
			)}
		</div>
	);
}

export default function PluginSequenceSheet({ open, onClose, plugins }: PluginSequenceSheetProps) {
	const [items, setItems] = useState<SequenceItem[]>([]);
	const [updatePlugin, { isLoading }] = useUpdatePluginMutation();
	const wasOpenRef = useRef(false);

	useEffect(() => {
		if (open && !wasOpenRef.current) {
			setItems(buildSequenceItems(plugins));
		}
		wasOpenRef.current = open;
	}, [open, plugins]);

	const handleSave = useCallback(async () => {
		const builtinIndex = items.findIndex((item) => item.type === "builtin");
		if (builtinIndex === -1) return;

		const updates: { name: string; placement: string; order: number }[] = [];

		items.forEach((item, index) => {
			if (item.type !== "custom" || !item.plugin) return;

			const placement = index < builtinIndex ? "pre_builtin" : "post_builtin";
			const groupItems = items.filter((it, i) => it.type === "custom" && (index < builtinIndex ? i < builtinIndex : i > builtinIndex));
			const order = groupItems.findIndex((it) => it.id === item.id);

			if (item.plugin.placement !== placement || item.plugin.order !== order) {
				updates.push({ name: item.plugin.name, placement, order });
			}
		});

		if (updates.length === 0) {
			onClose();
			return;
		}

		try {
			for (const u of updates) {
				const plugin = items.find((i) => i.id === u.name)?.plugin;
				await updatePlugin({
					name: u.name,
					data: {
						enabled: plugin?.enabled ?? true,
						config: plugin?.config,
						path: plugin?.path,
						placement: u.placement,
						order: u.order,
					},
				}).unwrap();
			}
			toast.success("Plugin sequence updated");
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [items, updatePlugin, onClose]);

	return (
		<Sheet open={open} onOpenChange={onClose}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8">
				<SheetHeader className="flex flex-col items-start p-0">
					<SheetTitle>Edit Plugin Sequence</SheetTitle>
					<SheetDescription>Drag plugins above or below the built-in plugins block to control execution order.</SheetDescription>
				</SheetHeader>

				<div className="mt-4 flex flex-1 flex-col gap-2">
					<DragDropProvider
						onDragOver={(event) => {
							const { source, target } = event.operation;
							if (!source || !target || source.id === target.id) return;

							setItems((current) => {
								const sourceIndex = current.findIndex((item) => item.id === source.id);
								const targetIndex = current.findIndex((item) => item.id === target.id);
								if (sourceIndex === -1 || targetIndex === -1 || sourceIndex === targetIndex) return current;

								const newItems = [...current];
								const [movedItem] = newItems.splice(sourceIndex, 1);
								newItems.splice(targetIndex, 0, movedItem);
								return newItems;
							});
						}}
					>
						{items.map((item, index) => (
							<SortableBlock key={item.id} item={item} index={index} />
						))}
					</DragDropProvider>
				</div>

				<div className="flex flex-col gap-2">
					<Alert variant="info">
						<AlertDescription>
							If your config.json file has plugin sequence configured, it will take precedence over the sequence configured in the UI after
							restarting Bifrost.
						</AlertDescription>
					</Alert>
					<div className="flex justify-end gap-2 pt-4">
						<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="plugin-sequence-cancel-button">
							Cancel
						</Button>
						<Button onClick={handleSave} disabled={isLoading} isLoading={isLoading} data-testid="plugin-sequence-save-button">
							Save Sequence
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}
