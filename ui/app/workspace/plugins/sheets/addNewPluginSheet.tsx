import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useT } from "@/lib/i18n";
import { getErrorMessage, useCreatePluginMutation, useUpdatePluginMutation } from "@/lib/store";
import { Plugin } from "@/lib/types/plugins";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { PluginFormFragment } from "../fragments/pluginFormFragments";

type PluginFormData = {
	name: string;
	path: string;
	hasConfig: boolean;
	config?: string;
};

interface AddNewPluginSheetProps {
	open: boolean;
	onClose: () => void;
	onCreate?: (pluginName: string) => void;
	plugin?: Plugin | null;
}

export default function AddNewPluginSheet({ open, onClose, onCreate, plugin }: AddNewPluginSheetProps) {
	const t = useT();
	const pluginFormSchema = useMemo(
		() =>
			z.object({
				name: z
					.string()
					.min(1, t("routing.plugins.nameRequired"))
					.regex(/^[A-Za-z0-9-_]+$/, t("routing.plugins.nameFormat")),
				path: z
					.string()
					.min(1, t("routing.plugins.pathRequired"))
					.refine(
						(val) => {
							return val.startsWith("/") || val.startsWith("http://") || val.startsWith("https://");
						},
						{
							message: t("routing.plugins.pathInvalid"),
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
							message: t("routing.plugins.configInvalidJson"),
						},
					),
			}),
		[t],
	);
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
				} catch {
					toast.error(t("routing.plugins.invalidJson"));
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
				toast.success(t("routing.plugins.updated"));
			} else {
				// Create new plugin
				await createPlugin({
					name: data.name,
					path: data.path,
					enabled: true,
					config: parsedConfig,
				}).unwrap();
				toast.success(t("routing.plugins.created"));
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
			<SheetContent className="flex w-full flex-col overflow-x-hidden pt-4">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky top-0 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{isEditMode ? t("routing.plugins.update") : t("routing.plugins.install")}</SheetTitle>
					<SheetDescription>{isEditMode ? t("routing.plugins.updateDesc") : t("routing.plugins.installDesc")}</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="flex-1 space-y-4 px-4 md:px-8">
							<PluginFormFragment form={form} isEditMode={isEditMode} />
						</div>

						<div className="bg-card sticky bottom-0 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={handleClose} disabled={isLoading}>
								{t("common.cancel")}
							</Button>
							<Button type="submit" disabled={isLoading || !form.formState.isValid || disableAction} isLoading={isLoading}>
								{isEditMode ? t("routing.plugins.update") : t("routing.plugins.installPlugin")}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}