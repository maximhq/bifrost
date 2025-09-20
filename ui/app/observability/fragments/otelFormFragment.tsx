"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { otelFormSchema, type OtelFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";

interface OtelFormFragmentProps {
	currentConfig?: {
		enabled?: boolean;
		push_url?: string;
		type?: "otel" | "genai_extension" | "vercel" | "arize_otel";
	};
	onSave: (config: OtelFormSchema) => Promise<void>;
	isLoading?: boolean;
}

export function OtelFormFragment({ currentConfig: initialConfig, onSave, isLoading = false }: OtelFormFragmentProps) {
	const [isSaving, setIsSaving] = useState(false);
	const form = useForm<OtelFormSchema, any, OtelFormSchema>({
		resolver: zodResolver(otelFormSchema) as Resolver<OtelFormSchema, any, OtelFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			enabled: initialConfig?.enabled || false,
			otel_config: {
				push_url: initialConfig?.push_url || "",
				type: initialConfig?.type || "otel",
			},
		},
	});

	const onSubmit = (data: OtelFormSchema) => {
		setIsSaving(true);
		onSave(data).finally(() => setIsSaving(false));
	};

	useEffect(() => {
		// Reset form with new initial config when it changes
		form.reset({
			enabled: initialConfig?.enabled || false,
			otel_config: {
				push_url: initialConfig?.push_url || "",
				type: initialConfig?.type || "otel",
			},
		});
	}, [form, initialConfig]);

	const traceTypeOptions = [
		{ value: "otel", label: "OTEL" },
		{ value: "genai_extension", label: "OTEL - GenAI Extension" },
		{ value: "vercel", label: "Vercel Style" },
		{ value: "arize_otel", label: "Arize OTEL" },
	];

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				{/* OTEL Configuration */}
				<div className="space-y-4">
					<div className="flex flex-row gap-4">
						<FormField
							control={form.control}
							name="otel_config.push_url"
							render={({ field }) => (
								<FormItem className="w-full">
									<FormLabel>Push URL</FormLabel>
									<FormControl>
										<Input placeholder="https://otel-collector.example.com:4318/v1/traces" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>

						<FormField
							control={form.control}
							name="otel_config.type"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Format</FormLabel>
									<Select onValueChange={field.onChange} defaultValue={field.value}>
										<FormControl>
											<SelectTrigger className="w-[200px]">
												<SelectValue placeholder="Select trace type" />
											</SelectTrigger>
										</FormControl>
										<SelectContent>
											{traceTypeOptions.map((option) => (
												<SelectItem key={option.value} value={option.value}>
													{option.label}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex w-full flex-row items-center">
					<FormField
						control={form.control}
						name="enabled"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center gap-2">
								<FormLabel>Enabled</FormLabel>
								<Switch checked={form.watch("enabled")} onCheckedChange={field.onChange} disabled={isLoading || !form.formState.isValid} />
							</FormItem>
						)}
					/>
					<div className="ml-auto flex justify-end space-x-2 py-2">
						<Button
							type="button"
							variant="outline"
							onClick={() => {
								form.reset({
									enabled: false,
									otel_config: undefined,
								});
							}}
							disabled={isLoading || !form.formState.isDirty}
						>
							Reset
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button type="submit" disabled={!form.formState.isDirty || !form.formState.isValid} isLoading={isSaving}>
										Save OTEL Configuration
									</Button>
								</TooltipTrigger>
								{(!form.formState.isDirty || !form.formState.isValid) && (
									<TooltipContent>
										<p>
											{!form.formState.isDirty && !form.formState.isValid
												? "No changes made and validation errors present"
												: !form.formState.isDirty
													? "No changes made"
													: "Please fix validation errors"}
										</p>
									</TooltipContent>
								)}
							</Tooltip>
						</TooltipProvider>
					</div>
				</div>
			</form>
		</Form>
	);
}
