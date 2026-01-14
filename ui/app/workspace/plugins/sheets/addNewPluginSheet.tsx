"use client";

import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage, useCreatePluginMutation, useUpdatePluginMutation } from "@/lib/store";
import { Plugin } from "@/lib/types/plugins";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { PluginFormFragment } from "../fragments/pluginFormFragments";

const pluginFormSchema = z.object({
	name: z
		.string()
		.min(1, "Plugin name is required")
		.regex(/^[A-Za-z0-9-_]+$/, "Plugin name must contain only letters, numbers, hyphens, and underscores"),
	path: z
		.string()
		.min(1, "Plugin path/URL is required")
		.refine(
			(val) => {
				// Accept either absolute file paths or HTTP/HTTPS URLs
				return val.startsWith("/") || val.startsWith("http://") || val.startsWith("https://");
			},
			{
				message: "Please enter a valid absolute file path (starting with /) or HTTP/HTTPS URL",
			},
		),
	hasConfig: z.boolean(),
	config: z
		.string()
		.optional()
		.refine(
			(val) => {
				if (!val) return true;
				try {
					JSON.parse(val);
					return true;
				} catch {
					return false;
				}
			},
			{
				message: "Configuration must be valid JSON",
			},
		),
});

type PluginFormData = z.infer<typeof pluginFormSchema>;

interface AddNewPluginSheetProps {
	open: boolean;
	onClose: () => void;
	onCreate?: (pluginName: string) => void;
	plugin?: Plugin | null;
}

export default function AddNewPluginSheet({ open, onClose, onCreate, plugin }: AddNewPluginSheetProps) {
	const hasCreatePluginAccess = useRbac(RbacResource.Plugins, RbacOperation.Create);
	const hasUpdatePluginAccess = useRbac(RbacResource.Plugins, RbacOperation.Update);
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();

	const isEditMode = !!plugin;
	const isLoading = isCreating || isUpdating;

	const form = useForm<PluginFormData>({
		resolver: zodResolver(pluginFormSchema),
		mode: "onChange",
		defaultValues: {
			name: "",
			path: "",
			hasConfig: false,
			config: undefined,
		},
	});

	// Load plugin data when editing
	useEffect(() => {
		if (plugin) {
			const hasConfig = plugin.config && Object.keys(plugin.config).length > 0;
			form.reset({
				name: plugin.name,
				path: plugin.path || "",
				hasConfig,
				config: hasConfig ? JSON.stringify(plugin.config, null, 2) : undefined,
			});
		} else {
			form.reset({
				name: "",
				path: "",
				hasConfig: false,
				config: undefined,
			});
		}
	}, [plugin, form]);

	const onSubmit = async (data: PluginFormData) => {
		try {
			let parsedConfig = {};

			if (data.hasConfig && data.config) {
				try {
					parsedConfig = JSON.parse(data.config);
				} catch (error) {
					toast.error("Invalid JSON configuration");
					return;
				}
			}

			if (isEditMode && plugin) {
				// Update existing plugin
				await updatePlugin({
					name: plugin.name,
					data: {
						enabled: plugin.enabled,
						config: parsedConfig,
					},
				}).unwrap();
				toast.success("Plugin updated successfully");
		} else {
			// Create new plugin
			await createPlugin({
				name: data.name,
				path: data.path,
				enabled: true,
				config: parsedConfig,
			}).unwrap();
			toast.success("Plugin created successfully");
			// Notify parent with the config name to select it
			onCreate?.(data.name);
		}

		form.reset();
		onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleClose = () => {
		form.reset();
		onClose();
	};

	const disableAction = isEditMode ? !hasUpdatePluginAccess : !hasCreatePluginAccess;

	return (
		<Sheet open={open} onOpenChange={handleClose}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8">
				<SheetHeader className="p-0 flex flex-col items-start">
					<SheetTitle>{isEditMode ? "Update Plugin" : "Install New Plugin"}</SheetTitle>
					<SheetDescription>
						{isEditMode
							? "Update the plugin configuration. Note: Plugin name and path cannot be changed."
							: "Add a custom plugin by providing its name, path/URL, and optional configuration."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="flex-1 space-y-4">
							<PluginFormFragment form={form} isEditMode={isEditMode} />
						</div>

						<div className="flex justify-end gap-2">
							<Button type="button" variant="outline" onClick={handleClose} disabled={isLoading}>
								Cancel
							</Button>
							<Button type="submit" disabled={isLoading || !form.formState.isValid || disableAction} isLoading={isLoading}>
								{isEditMode ? "Update Plugin" : "Install Plugin"}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}
