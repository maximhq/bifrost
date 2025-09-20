"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { otelFormSchema, type OtelFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface OtelFormFragmentProps {
	initialConfig?: {
		push_url?: string;
		trace_type?: "traditional" | "genai" | "vercel" | "arize_otel";
	};
	onSave?: (config: OtelFormSchema) => void;
	isLoading?: boolean;
}

export function OtelFormFragment({ initialConfig, onSave, isLoading = false }: OtelFormFragmentProps) {
	const form = useForm<OtelFormSchema, any, OtelFormSchema>({
		resolver: zodResolver(otelFormSchema) as Resolver<OtelFormSchema, any, OtelFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			otel_config: {
				push_url: initialConfig?.push_url || "",
				trace_type: initialConfig?.trace_type || "traditional",
			},
		},
	});

	const onSubmit = (data: OtelFormSchema) => {
		if (onSave) {
			onSave(data);
		} else {
			// Default behavior - show success toast
			toast.success("OTEL configuration saved successfully");
			console.log("OTEL Config:", data);
		}
	};

	useEffect(() => {
		// Reset form with new initial config when it changes
		form.reset({
			otel_config: {
				push_url: initialConfig?.push_url || "",
				trace_type: initialConfig?.trace_type || "traditional",
			},
		});
	}, [form, initialConfig]);

	const traceTypeOptions = [
		{ value: "traditional", label: "Traditional" },
		{ value: "genai", label: "GenAI" },
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
							name="otel_config.trace_type"
							render={({ field }) => (
								<FormItem >
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
				<div className="flex justify-end space-x-2 py-2">
					<Button
						type="button"
						variant="outline"
						onClick={() => {
							form.reset({
								otel_config: {
									push_url: "",
									trace_type: "traditional",
								},
							});
						}}
						disabled={isLoading || !form.formState.isDirty}
					>
						Reset
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button type="submit" disabled={!form.formState.isDirty || !form.formState.isValid} isLoading={isLoading}>
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
			</form>
		</Form>
	);
}
