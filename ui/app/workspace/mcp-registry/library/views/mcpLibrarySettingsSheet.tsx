import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage, useForceSyncMCPLibraryMutation, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

const mcpLibrarySettingsSchema = z.object({
	mcp_library_url: z
		.string()
		.trim()
		.refine(
			(value) => value === "" || value.startsWith("http://") || value.startsWith("https://"),
			"URL must start with http:// or https://",
		),
	mcp_library_sync_interval_hours: z
		.number({ message: "Sync interval is required" })
		.min(1, "Sync interval must be at least 1 hour")
		.max(8760, "Sync interval cannot exceed 8760 hours (1 year)"),
});

type MCPLibrarySettingsFormData = z.infer<typeof mcpLibrarySettingsSchema>;

interface MCPLibrarySettingsSheetProps {
	open: boolean;
	onClose: () => void;
}

export function MCPLibrarySettingsSheet({ open, onClose }: MCPLibrarySettingsSheetProps) {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig, isLoading: isConfigLoading, isError: isConfigError } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.framework_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [forceSyncMCPLibrary, { isLoading: isForceSyncing }] = useForceSyncMCPLibraryMutation();

	const {
		register,
		handleSubmit,
		formState: { errors, isDirty },
		reset,
		watch,
	} = useForm<MCPLibrarySettingsFormData>({
		resolver: zodResolver(mcpLibrarySettingsSchema),
		defaultValues: {
			mcp_library_url: "",
			mcp_library_sync_interval_hours: 24,
		},
	});

	const formValues = watch();

	useEffect(() => {
		if (!open || !config) return;
		reset({
			mcp_library_url: config.mcp_library_url || "",
			mcp_library_sync_interval_hours: Math.round((config.mcp_library_sync_interval || 86400) / 3600),
		});
	}, [config, open, reset]);

	const hasChanges = useMemo(() => {
		if (!config || !isDirty) return false;
		const serverUrl = config.mcp_library_url || "";
		const serverInterval = Math.round((config.mcp_library_sync_interval || 86400) / 3600);
		return formValues.mcp_library_url !== serverUrl || formValues.mcp_library_sync_interval_hours !== serverInterval;
	}, [config, formValues, isDirty]);

	const onSubmit = async (data: MCPLibrarySettingsFormData) => {
		if (!bifrostConfig) {
			toast.error("Unable to load current settings. Please retry.");
			return;
		}
		try {
			await updateCoreConfig({
				...bifrostConfig,
				framework_config: {
					...bifrostConfig.framework_config,
					mcp_library_url: data.mcp_library_url,
					mcp_library_sync_interval: data.mcp_library_sync_interval_hours * 3600,
				},
			}).unwrap();
			toast.success("MCP Library settings updated successfully.");
			reset(data);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleForceSync = async () => {
		try {
			await forceSyncMCPLibrary().unwrap();
			toast.success("MCP Library sync triggered successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden px-0">
				<SheetHeader className="flex flex-col items-start px-7 pt-8">
					<SheetTitle>MCP Library Settings</SheetTitle>
					<SheetDescription>Configure the sync source and interval for the MCP server catalog.</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
					<div className="flex-1 space-y-4 overflow-y-auto px-8">
						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="mcp-library-url">Library Sync URL</Label>
								<p className="text-muted-foreground text-sm">
									URL to a custom MCP server catalog. Leave empty to use the default Bifrost catalog.
								</p>
							</div>
							<Input
								id="mcp-library-url"
								type="text"
								placeholder="https://getbifrost.ai/mcp-library"
								data-testid="mcp-library-url-input"
								{...register("mcp_library_url")}
								className={errors.mcp_library_url ? "border-destructive" : ""}
							/>
							{errors.mcp_library_url && <p className="text-destructive text-sm">{errors.mcp_library_url.message}</p>}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="mcp-library-sync-interval">Sync Interval (hours)</Label>
								<p className="text-muted-foreground text-sm">How often to sync the MCP server catalog from the source URL.</p>
							</div>
							<Input
								id="mcp-library-sync-interval"
								type="number"
								data-testid="mcp-library-sync-interval-input"
								className={errors.mcp_library_sync_interval_hours ? "border-destructive" : ""}
								{...register("mcp_library_sync_interval_hours", { valueAsNumber: true })}
							/>
							{errors.mcp_library_sync_interval_hours && (
								<p className="text-destructive text-sm">{errors.mcp_library_sync_interval_hours.message}</p>
							)}
						</div>
					</div>

					<div className="dark:bg-card border-border border-t bg-white px-8 py-4">
						<div className="flex justify-end gap-2">
							<Button
								variant="outline"
								type="button"
								onClick={handleForceSync}
								disabled={isForceSyncing || !hasSettingsUpdateAccess}
								data-testid="mcp-library-force-sync-btn"
							>
								{isForceSyncing ? "Syncing..." : "Force Sync Now"}
							</Button>
							<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="mcp-library-settings-cancel-btn">
								Cancel
							</Button>
							<Button
								type="submit"
								disabled={!hasChanges || isLoading || isConfigLoading || isConfigError || !hasSettingsUpdateAccess}
								data-testid="mcp-library-settings-save-btn"
							>
								{isLoading ? "Saving..." : "Save Changes"}
							</Button>
						</div>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}
