import ConfirmDeletePluginDialog from "@/app/workspace/plugins/dialogs/confirmDeletePluginDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { setPluginFormDirtyState, useAppDispatch, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { PluginType } from "@/lib/types/plugins";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { PlusIcon, SaveIcon, Trash2Icon } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import * as z from "zod";
import i18n from "@/lib/i18n";

interface Props {
	onDelete: () => void;
	onCreate: (pluginName: string) => void;
}

const pluginFormSchema = z.object({
	name: z.string().min(1, "Name is required"),
	enabled: z.boolean(),
	path: z.string().optional(),
	config: z.string().optional(),
	hasConfig: z.boolean(),
});

type PluginFormValues = z.infer<typeof pluginFormSchema>;

const getPluginTypeColor = (type: PluginType) => {
	switch (type) {
		case "llm":
			return "bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-300";
		case "mcp":
			return "bg-purple-100 text-purple-800 dark:bg-purple-900/30 dark:text-purple-300";
		case "http":
			return "bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-300";
		default:
			return "bg-gray-100 text-gray-800 dark:bg-gray-900/30 dark:text-gray-300";
	}
};

export default function PluginsView(props: Props) {
	const dispatch = useAppDispatch();
	const hasUpdatePluginAccess = useRbac(RbacResource.Plugins, RbacOperation.Update);
	const hasDeletePluginAccess = useRbac(RbacResource.Plugins, RbacOperation.Delete);
	const [updatePlugin, { isLoading }] = useUpdatePluginMutation();
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const [showConfig, setShowConfig] = useState(false);
	const [showDeleteDialog, setShowDeleteDialog] = useState(false);

	const form = useForm<PluginFormValues>({
		resolver: zodResolver(pluginFormSchema),
		defaultValues: {
			name: selectedPlugin?.name || "",
			enabled: selectedPlugin?.enabled || false,
			path: selectedPlugin?.path || undefined,
			config: selectedPlugin?.config ? JSON.stringify(selectedPlugin.config, null, 2) : undefined,
			hasConfig: Boolean(selectedPlugin?.config && Object.keys(selectedPlugin.config).length > 0),
		},
	});

	// Update form when selectedPlugin changes
	useEffect(() => {
		if (selectedPlugin) {
			const hasConfig = Boolean(selectedPlugin.config && Object.keys(selectedPlugin.config).length > 0);
			setShowConfig(hasConfig);
			form.reset({
				name: selectedPlugin.name,
				enabled: selectedPlugin.enabled,
				path: selectedPlugin.path,
				config: hasConfig ? JSON.stringify(selectedPlugin.config, null, 2) : undefined,
				hasConfig,
			});
		}
	}, [selectedPlugin]);

	// Track form dirty state
	useEffect(() => {
		const isDirty = form.formState.isDirty;
		dispatch(setPluginFormDirtyState(isDirty));
	}, [form.formState.isDirty, dispatch]);

	const onSubmit = async (values: PluginFormValues) => {
		if (!selectedPlugin) return;

		try {
			let config;
			if (values.hasConfig && values.config) {
				try {
					config = JSON.parse(values.config);
				} catch {
					toast.error(i18n.t("workspace.plugins.invalidJsonConfiguration"));
					return;
				}
			}

			await updatePlugin({
				name: selectedPlugin.name,
				data: {
					enabled: values.enabled,
					path: values.path ?? undefined,
					...(config !== undefined && { config }),
				},
			}).unwrap();
			toast.success(i18n.t("workspace.plugins.pluginUpdated"));
			form.reset(values);
		} catch {
			toast.error(i18n.t("workspace.plugins.failedToUpdatePlugin"));
		}
	};

	const onError = () => {
		toast.error(i18n.t("workspace.plugins.fixFormErrors"));
	};

	const handleDeleteClick = () => {
		setShowDeleteDialog(true);
	};

	const handleDeleteCancel = () => {
		setShowDeleteDialog(false);
	};

	const handleDeleteSuccess = () => {
		setShowDeleteDialog(false);
		toast.success(i18n.t("workspace.plugins.pluginDeleted"));
		props.onDelete();
	};

	if (!selectedPlugin) {
		return (
			<div className="ml-4 flex w-full items-center justify-center">
				<p className="text-muted-foreground">{i18n.t("workspace.plugins.noPluginSelected")}</p>
			</div>
		);
	}

	const isErrorLog = (log: string) => {
		const errorKeywords = ["error", "failed", "exception", "panic", "fatal", "ERR"];
		return errorKeywords.some((keyword) => log.toLowerCase().includes(keyword.toLowerCase()));
	};

	return (
		<div className="ml-4 w-full">
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit, onError)} className="space-y-6">
					<div className="">
						<h3 className="mb-4 text-lg font-semibold">{i18n.t("workspace.plugins.pluginConfiguration")}</h3>
						<div className="space-y-6">
							<FormField
								control={form.control}
								name="name"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{i18n.t("workspace.plugins.name")}</FormLabel>
										<FormControl>
											<Input
												placeholder={i18n.t("workspace.plugins.pluginName")}
												{...field}
												readOnly
												disabled
												className="cursor-not-allowed"
											/>
										</FormControl>
										<FormDescription>{i18n.t("workspace.plugins.nameDescription")}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							{selectedPlugin.status?.types && selectedPlugin.status.types.length > 0 && (
								<FormItem>
									<FormLabel>{i18n.t("workspace.plugins.types")}</FormLabel>
									<FormControl>
										<div className="flex flex-wrap gap-1">
											{selectedPlugin.status.types.map((type) => (
												<Badge
													key={type}
													variant="outline"
													className={cn("h-5 px-2 text-xs font-medium uppercase", getPluginTypeColor(type))}
												>
													{type}
												</Badge>
											))}
										</div>
									</FormControl>
								</FormItem>
							)}

							<FormField
								control={form.control}
								name="enabled"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between">
										<div className="space-y-0.5">
											<FormLabel>{i18n.t("workspace.plugins.enabled")}</FormLabel>
											<FormDescription>{i18n.t("workspace.plugins.enableOrDisablePlugin")}</FormDescription>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>

							<FormField
								control={form.control}
								name="path"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{i18n.t("workspace.plugins.path")}</FormLabel>
										<FormControl>
											<Input placeholder={i18n.t("workspace.plugins.pluginPath")} {...field} value={field.value || ""} />
										</FormControl>
										<FormDescription>{i18n.t("workspace.plugins.pathDescription")}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							{!showConfig ? (
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => {
										setShowConfig(true);
										form.setValue("hasConfig", true);
										if (!form.getValues("config")) {
											form.setValue("config", "{}");
										}
									}}
									className="w-full"
								>
									<PlusIcon className="mr-2 h-4 w-4" />
									{i18n.t("workspace.plugins.addConfiguration")}
								</Button>
							) : (
								<FormField
									control={form.control}
									name="config"
									render={({ field }) => (
										<FormItem>
											<div className="flex items-center justify-between">
												<FormLabel>{i18n.t("workspace.plugins.configurationJson")}</FormLabel>
												<Button
													type="button"
													variant="ghost"
													size="sm"
													onClick={() => {
														setShowConfig(false);
														form.setValue("hasConfig", false);
														form.setValue("config", undefined);
													}}
													className="h-auto p-1 text-xs"
												>
													{i18n.t("workspace.plugins.remove")}
												</Button>
											</div>
											<FormControl>
												<div className="rounded-sm border">
													<CodeEditor
														className="z-0 w-full"
														minHeight={200}
														maxHeight={400}
														wrap={true}
														code={field.value || "{}"}
														lang="json"
														onChange={field.onChange}
														options={{
															scrollBeyondLastLine: false,
															collapsibleBlocks: true,
															lineNumbers: "on",
															alwaysConsumeMouseWheel: false,
														}}
													/>
												</div>
											</FormControl>
											<FormDescription>{i18n.t("workspace.plugins.pluginConfigurationFormat")}</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>
							)}
						</div>

						{selectedPlugin.status?.status !== "active" && (
							<div className="mt-4">
								<div className="space-y-4">
									{selectedPlugin.status?.logs && selectedPlugin.status.logs.length > 0 && (
										<div className="grid gap-2">
											<label className="text-sm font-medium">{i18n.t("workspace.plugins.logs")}</label>
											<div className="rounded-md border px-4 py-2 font-mono text-xs">
												<div className="flex flex-row items-center gap-2">
													{selectedPlugin.status.logs.map((log, index) => (
														<div key={index} className={isErrorLog(log) ? "text-red-400" : "text-green-600"}>
															{log}
														</div>
													))}
												</div>
											</div>
										</div>
									)}
								</div>
							</div>
						)}
					</div>

					<div className="flex flex-wrap justify-end gap-2">
						<Button
							className="border-destructive text-destructive hover:bg-destructive/10 hover:text-destructive"
							type="button"
							variant="outline"
							onClick={handleDeleteClick}
							disabled={!hasDeletePluginAccess}
						>
							<Trash2Icon className="h-4 w-4" />
							{i18n.t("workspace.plugins.deletePlugin")}
						</Button>
						<Button
							type="button"
							variant="outline"
							onClick={() => form.reset()}
							disabled={!form.formState.isDirty || !hasUpdatePluginAccess}
						>
							{i18n.t("workspace.plugins.reset")}
						</Button>
						<Button type="submit" disabled={isLoading || !form.formState.isDirty || !hasUpdatePluginAccess}>
							<SaveIcon className="h-4 w-4" />
							{isLoading ? i18n.t("workspace.plugins.saving") : i18n.t("workspace.plugins.saveChanges")}
						</Button>
					</div>
				</form>
			</Form>

			{selectedPlugin && (
				<ConfirmDeletePluginDialog
					show={showDeleteDialog}
					onCancel={handleDeleteCancel}
					onDelete={handleDeleteSuccess}
					plugin={selectedPlugin}
				/>
			)}
		</div>
	);
}