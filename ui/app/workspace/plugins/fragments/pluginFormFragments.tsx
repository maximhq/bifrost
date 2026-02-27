"use client";

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import { Button } from "@/components/ui/button";
import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Info, PlusIcon } from "lucide-react";
import { useState } from "react";
import { UseFormReturn } from "react-hook-form";

interface PluginFormData {
	name: string;
	path: string;
	config?: string;
	hasConfig: boolean;
}

interface PluginFormFragmentProps {
	form: UseFormReturn<PluginFormData>;
	isEditMode?: boolean;
}

export function PluginFormFragment({ form, isEditMode = false }: PluginFormFragmentProps) {
	const [showConfig, setShowConfig] = useState(form.getValues("hasConfig") || false);

	return (
		<div className="space-y-4">
			<div className="bg-muted/50 flex items-start gap-2 rounded-md border p-3">
				<Info className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />
				<p className="text-muted-foreground text-sm">
					{isEditMode
						? "Update your plugin configuration. Plugin name and path are read-only."
						: "Install a custom plugin by providing an absolute file path or HTTP URL accessible to Bifrost deployment (.so)."}{" "}
					<a
						href="https://docs.getbifrost.ai/plugins"
						target="_blank"
						rel="noopener noreferrer"
						className="text-primary hover:underline"
						data-testid="plugins-form-docs-link"
					>
						Learn more
					</a>
				</p>
			</div>

			<FormField
				control={form.control}
				name="name"
				render={({ field }) => (
					<FormItem>
						<FormLabel>Plugin Name *</FormLabel>
						<FormControl>
							<Input placeholder="e.g., my-custom-plugin" {...field} disabled={isEditMode} />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>

			<FormField
				control={form.control}
				name="path"
				render={({ field }) => (
					<FormItem>
						<FormLabel>Plugin Path/URL *</FormLabel>
						<FormControl>
							<Input placeholder="e.g., /path/to/plugin.so or https://example.com/plugin.so" {...field} disabled={isEditMode} />
						</FormControl>
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
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
		</div>
	);
}
