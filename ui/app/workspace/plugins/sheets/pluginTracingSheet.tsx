import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { getErrorMessage, useGetBuiltinPluginsQuery, useGetPluginQuery, useGetPluginsQuery, useUpdatePluginMutation } from "@/lib/store";
import { PluginSpanFilter } from "@/lib/types/config";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";

interface PluginTracingSheetProps {
	open: boolean;
	onClose: () => void;
}

function resolveToggleState(filter: PluginSpanFilter | null | undefined, allPlugins: string[]): Record<string, boolean> {
	const state: Record<string, boolean> = {};
	for (const name of allPlugins) {
		state[name] = true;
	}
	if (!filter) return state;

	if (filter.mode === "exclude") {
		for (const name of filter.plugins) {
			state[name] = false;
		}
	} else {
		for (const name of allPlugins) {
			state[name] = filter.plugins.includes(name);
		}
	}
	return state;
}

function buildFilter(toggles: Record<string, boolean>): PluginSpanFilter | null {
	const excluded = Object.entries(toggles)
		.filter(([, on]) => !on)
		.map(([name]) => name);
	if (excluded.length === 0) return null;
	return { mode: "exclude", plugins: excluded };
}

function PluginRow({ name, checked, onChange }: { name: string; checked: boolean; onChange: (v: boolean) => void }) {
	return (
		<div className="flex items-center justify-between rounded-md border px-3 py-2.5">
			<span className="text-sm font-mono">{name}</span>
			<div className="flex items-center gap-2">
				<Switch checked={checked} onCheckedChange={onChange} data-testid={`plugin-tracing-toggle-${name}`} />
			</div>
		</div>
	);
}

export default function PluginTracingSheet({ open, onClose }: PluginTracingSheetProps) {
	const { data: builtinPluginNames = [] } = useGetBuiltinPluginsQuery();
	const { data: allPluginsData } = useGetPluginsQuery();
	const customPluginNames = (allPluginsData ?? []).filter((p) => p.isCustom).map((p) => p.name);
	const allPlugins = [...builtinPluginNames, ...customPluginNames];
	const { data: otelPlugin } = useGetPluginQuery("otel");
	const [updatePlugin, { isLoading }] = useUpdatePluginMutation();
	const [toggles, setToggles] = useState<Record<string, boolean>>({});
	const wasOpenRef = useRef(false);

	useEffect(() => {
		if (open && !wasOpenRef.current) {
			if (!otelPlugin) return; // wait until persisted config is available
			const filter = (otelPlugin.config?.plugin_span_filter as PluginSpanFilter | undefined) ?? null;
			setToggles(resolveToggleState(filter, allPlugins));
			wasOpenRef.current = true;
		}
		if (!open) wasOpenRef.current = false;
	}, [open, otelPlugin, allPlugins]);

	const setToggle = useCallback((name: string, value: boolean) => {
		setToggles((prev) => ({ ...prev, [name]: value }));
	}, []);

	const handleSave = useCallback(async () => {
		if (!otelPlugin) {
			toast.error("OTEL plugin not found");
			return;
		}
		const filter = buildFilter(toggles);
		try {
			await updatePlugin({
				name: "otel",
				data: {
					enabled: otelPlugin.enabled,
					config: { plugin_span_filter: filter },
				},
			}).unwrap();
			toast.success("Plugin tracing configuration saved");
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [toggles, otelPlugin, updatePlugin, onClose]);

	return (
		<Sheet open={open} onOpenChange={onClose}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-8">
				<SheetHeader className="flex flex-col items-start p-0">
					<SheetTitle>Configure Plugin Tracing</SheetTitle>
					<SheetDescription>
						Choose which plugin hook spans are exported to the OTEL collector. Disabling a plugin removes its spans from traces without
						affecting execution.
					</SheetDescription>
				</SheetHeader>

				<div className="mt-4 flex-1 overflow-y-auto">
					<div className="flex flex-col gap-4">
						<div>
							<div className="mb-2 flex items-center justify-between">
								<p className="text-muted-foreground text-xs font-medium uppercase tracking-wide">Built-in Plugins</p>
								<TriStateCheckbox
									allIds={builtinPluginNames}
									selectedIds={builtinPluginNames.filter((n) => toggles[n] ?? true)}
									onChange={(next) => {
										const nextSet = new Set(next);
										setToggles((prev) => {
											const updated = { ...prev };
											for (const n of builtinPluginNames) updated[n] = nextSet.has(n);
											return updated;
										});
									}}
									ariaLabel="Toggle all built-in plugin tracing"
									data-testid="plugin-tracing-select-all-builtins"
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								{builtinPluginNames.map((name) => (
									<PluginRow key={name} name={name} checked={toggles[name] ?? true} onChange={(v) => setToggle(name, v)} />
								))}
							</div>
						</div>

						{customPluginNames.length > 0 && (
							<div>
								<div className="mb-2 flex items-center justify-between">
									<p className="text-muted-foreground text-xs font-medium uppercase tracking-wide">Custom Plugins</p>
									<TriStateCheckbox
										allIds={customPluginNames}
										selectedIds={customPluginNames.filter((n) => toggles[n] ?? true)}
										onChange={(next) => {
											const nextSet = new Set(next);
											setToggles((prev) => {
												const updated = { ...prev };
												for (const n of customPluginNames) updated[n] = nextSet.has(n);
												return updated;
											});
										}}
										ariaLabel="Toggle all custom plugin tracing"
										data-testid="plugin-tracing-select-all-custom"
									/>
								</div>
								<div className="flex flex-col gap-1.5">
									{customPluginNames.map((name) => (
										<PluginRow key={name} name={name} checked={toggles[name] ?? true} onChange={(v) => setToggle(name, v)} />
									))}
								</div>
							</div>
						)}
					</div>
				</div>

				<div className="flex flex-col gap-2 pt-4">
					<Alert variant="info">
						<AlertDescription>
							<span>
								If <strong className="inline">plugin_span_filter</strong> is set inside the OTEL plugin config in config.json, it takes precedence over these settings after restarting Bifrost.
							</span>
						</AlertDescription>
					</Alert>
					<div className="flex justify-end gap-2 pt-2">
						<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="plugin-tracing-cancel-button">
							Cancel
						</Button>
						<Button onClick={handleSave} disabled={isLoading} isLoading={isLoading} data-testid="plugin-tracing-save-button" type="button">
							Save
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}
