"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { maximFormSchema, type MaximFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

interface MaximFormFragmentProps {
	initialConfig?: {
		api_key?: string;
		log_repo_id?: string;
	};
	onSave?: (config: MaximFormSchema) => void;
	showRestartAlert?: boolean;
	isLoading?: boolean;
}

export function MaximFormFragment({ initialConfig, onSave, showRestartAlert = false, isLoading = false }: MaximFormFragmentProps) {
	const [showApiKey, setShowApiKey] = useState(false);

	const form = useForm<MaximFormSchema, any, MaximFormSchema>({
		resolver: zodResolver(maximFormSchema) as Resolver<MaximFormSchema, any, MaximFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			maxim_config: {
				api_key: initialConfig?.api_key || "",
				log_repo_id: initialConfig?.log_repo_id || "",
			},
		},
	});

	const onSubmit = (data: MaximFormSchema) => {
		if (onSave) {
			onSave(data);
		} else {
			// Default behavior - show success toast
			toast.success("Maxim configuration saved successfully");
			console.log("Maxim Config:", data);
		}
	};

	useEffect(() => {
		// Reset form with new initial config when it changes
		form.reset({
			maxim_config: {
				api_key: initialConfig?.api_key || "",
				log_repo_id: initialConfig?.log_repo_id || "",
			},
		});
	}, [form, initialConfig]);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				{showRestartAlert && form.formState.isDirty && (
					<Alert>
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							The settings below require a Bifrost service restart to take effect. Current connections will continue with existing settings
							until restart.
						</AlertDescription>
					</Alert>
				)}

				{/* Maxim Configuration */}
				<div className="space-y-4">
					<div className="grid grid-cols-1 gap-4">
						<FormField
							control={form.control}
							name="maxim_config.api_key"
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key</FormLabel>
									<FormControl>
										<div className="relative">
											<Input type={showApiKey ? "text" : "password"} placeholder="Enter your Maxim API key" {...field} className="pr-10" />
											<Button
												type="button"
												variant="ghost"
												size="sm"
												className="absolute top-0 right-0 h-full px-3 py-2 hover:bg-transparent"
												onClick={() => setShowApiKey(!showApiKey)}
											>
												{showApiKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
											</Button>
										</div>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>

						<FormField
							control={form.control}
							name="maxim_config.log_repo_id"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Log Repository ID (Optional)</FormLabel>
									<FormControl>
										<Input placeholder="Enter log repository ID" {...field} value={field.value || ""} />
									</FormControl>
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
								maxim_config: {
									api_key: "",
									log_repo_id: "",
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
									Save Maxim Configuration
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
