"use client";

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import ConfirmDeletePluginDialog from "@/app/workspace/plugins/dialogs/confirmDeletePluginDialog";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { setPluginFormDirtyState, useAppDispatch, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { zodResolver } from "@hookform/resolvers/zod";
import { PlusIcon, SaveIcon, Trash2Icon } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import * as z from "zod";

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

export default function PluginsView(props: Props) {
	const dispatch = useAppDispatch();
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
			hasConfig: selectedPlugin?.config && Object.keys(selectedPlugin.config).length > 0,
		},
	});

	// Update form when selectedPlugin changes
	useEffect(() => {
		if (selectedPlugin) {
			const hasConfig = selectedPlugin.config && Object.keys(selectedPlugin.config).length > 0;
			setShowConfig(hasConfig);
			form.reset({
				name: selectedPlugin.name,
				enabled: selectedPlugin.enabled,
				path: selectedPlugin.path,
				config: hasConfig ? JSON.stringify(selectedPlugin.config, null, 2) : undefined,
				hasConfig,
			});
		}
	}, [selectedPlugin, form]);

	// Track form dirty state
	useEffect(() => {
		const isDirty = form.formState.isDirty;
		dispatch(setPluginFormDirtyState(isDirty));
	}, [form.formState.isDirty, dispatch]);

	const onSubmit = async (values: PluginFormValues) => {
		if (!selectedPlugin) return;

		try {
			let config = {};
			if (values.hasConfig && values.config) {
				try {
					config = JSON.parse(values.config);
				} catch (e) {
					toast.error("Invalid JSON in configuration");
					return;
				}
			}

			await updatePlugin({
				name: selectedPlugin.name,
				data: {
					enabled: values.enabled,
					path: values.path ?? undefined,
					config,
				},
			}).unwrap();
			toast.success("Plugin updated successfully");
			form.reset(values);
		} catch (error) {
			toast.error("Failed to update plugin");
			console.error("Failed to update plugin:", error);
		}
	};

	const handleDeleteClick = () => {
		setShowDeleteDialog(true);
	};

	const handleDeleteCancel = () => {
		setShowDeleteDialog(false);
	};

	const handleDeleteSuccess = () => {
		setShowDeleteDialog(false);
		toast.success("Plugin deleted successfully");
		props.onDelete();
	};

	if (!selectedPlugin) {
		return (
			<div className="ml-4 flex w-full items-center justify-center">
				<p className="text-muted-foreground">No plugin selected</p>
			</div>
		);
	}

	const getStatusVariant = (status?: string) => {
		switch (status?.toLowerCase()) {
			case "active":
				return "success";
			case "error":
			case "failed":
				return "destructive";
			default:
				return "secondary";
		}
	};

	const isErrorLog = (log: string) => {
		const errorKeywords = ["error", "failed", "exception", "panic", "fatal", "ERR"];
		return errorKeywords.some((keyword) => log.toLowerCase().includes(keyword.toLowerCase()));
	};

	return (
		<div className="ml-4 w-full">
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
					{/* Editable Fields */}
					<div className="">
						<h3 className="mb-4 text-lg font-semibold">Plugin Configuration</h3>
						<div className="space-y-4">
							<FormField
								control={form.control}
								name="name"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Name</FormLabel>
										<FormControl>
											<Input placeholder="Plugin name" {...field} readOnly disabled className="cursor-not-allowed" />
										</FormControl>
										<FormDescription>The name of the plugin</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							<FormField
								control={form.control}
								name="enabled"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between">
										<div className="space-y-0.5">
											<FormLabel className="text-base">Enabled</FormLabel>
											<FormDescription>Enable or disable this plugin</FormDescription>
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
										<FormLabel>Path</FormLabel>
										<FormControl>
											<Input placeholder="Plugin path" {...field} value={field.value || ""} />
										</FormControl>
										<FormDescription>The file system path to the plugin</FormDescription>
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
									Add Configuration
								</Button>
							) : (
								<FormField
									control={form.control}
									name="config"
									render={({ field }) => (
										<FormItem>
											<div className="flex items-center justify-between">
												<FormLabel>Configuration (JSON)</FormLabel>
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
													Remove
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
											<FormDescription>Plugin configuration in JSON format</FormDescription>
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
											<label className="text-sm font-medium">Logs</label>
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

					<div className="flex justify-between gap-2">
						<Button
							className="border-destructive text-destructive hover:bg-destructive/10 hover:text-destructive"
							type="button"
							variant="outline"
							onClick={handleDeleteClick}
						>
							<Trash2Icon className="h-4 w-4" />
							Delete Plugin
						</Button>
						<div className="flex gap-2">
							<Button type="button" variant="outline" onClick={() => form.reset()} disabled={!form.formState.isDirty}>
								Reset
							</Button>
							<Button type="submit" disabled={isLoading || !form.formState.isDirty}>
								<SaveIcon className="h-4 w-4" />
								{isLoading ? "Saving..." : "Save Changes"}
							</Button>
						</div>
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
