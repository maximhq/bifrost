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
import { toIntervalHours } from "./mcpLibrarySettingsSheet.utils";

const mcpLibrarySettingsSchema = z.object({
	// file:// is accepted so air-gapped deployments can point the catalog at a
	// local file that ships with the image or volume, matching how the pricing
	// datasheet URLs are handled in modelSettingsView.
	mcp_library_url: z
		.string()
		.trim()
		.refine(
			(value) =>
				value === "" || value.startsWith("http://") || value.startsWith("https://") || value.startsWith("file://"),
			"URL must start with http://, https://, or file://",
		),
	// 0 disables background syncing entirely. Force Sync Now still works.
	// Anything else has to be at least an hour: transports/config.schema.json
	// declares mcp_library_sync_interval as `anyOf: [{const: 0}, {minimum:
	// 3600}]`, so a fractional value below 1h would be rejected server-side.
	mcp_library_sync_interval_hours: z
		.number({ message: "Sync interval is required" })
		.max(8760, "Sync interval cannot exceed 8760 hours (1 year)")
		.refine((hours) => hours === 0 || hours >= 1, "Sync interval must be 0 (disabled) or at least 1 hour"),
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
			mcp_library_sync_interval_hours: toIntervalHours(config.mcp_library_sync_interval),
		});
	}, [config, open, reset]);

	const hasChanges = useMemo(() => {
		if (!config || !isDirty) return false;
		const serverUrl = config.mcp_library_url || "";
		const serverInterval = toIntervalHours(config.mcp_library_sync_interval);
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
				<SheetHeader className="flex flex-col items-start px-4 pt-8 md:px-7">
					<SheetTitle>MCP Library Settings</SheetTitle>
					<SheetDescription>Configure the sync source and interval for the MCP server catalog.</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
					<div className="flex-1 space-y-4 overflow-y-auto px-4 md:px-8">
						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="mcp-library-url">Library Sync URL</Label>
								<p className="text-muted-foreground text-sm">
									URL to a custom MCP server catalog. Leave empty to use the default Bifrost catalog. Use a{" "}
									<code>file://</code> URL to load the catalog from local disk in air-gapped deployments.
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
								<p className="text-muted-foreground text-sm">
									How often to sync the MCP server catalog from the source URL. Set to 0 to disable background syncing;
									Force Sync Now still works.
								</p>
							</div>
							<Input
								id="mcp-library-sync-interval"
								type="number"
								// step="any" so a stored non-whole-hour interval (5400s = 1.5h is
								// valid config) stays editable instead of tripping the number
								// input's default step of 1.
								step="any"
								min={0}
								data-testid="mcp-library-sync-interval-input"
								className={errors.mcp_library_sync_interval_hours ? "border-destructive" : ""}
								{...register("mcp_library_sync_interval_hours", { valueAsNumber: true })}
							/>
							{errors.mcp_library_sync_interval_hours && (
								<p className="text-destructive text-sm">{errors.mcp_library_sync_interval_hours.message}</p>
							)}
						</div>
					</div>

					<div className="dark:bg-card border-border border-t bg-white px-4 py-4 md:px-8">
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