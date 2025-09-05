"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider, isKnownProvider } from "@/lib/types/config";
import { networkOnlyFormSchema, type NetworkOnlyFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface NetworkFormFragmentProps {
	provider: ModelProvider;
	showRestartAlert?: boolean;
}

export function NetworkFormFragment({ provider, showRestartAlert = false }: NetworkFormFragmentProps) {
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const isCustomProvider = !isKnownProvider(provider.name as string);

	const form = useForm<NetworkOnlyFormSchema>({
		resolver: zodResolver(networkOnlyFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			network_config: provider.network_config,
		},
	});

	const onSubmit = (data: NetworkOnlyFormSchema) => {
		if (isCustomProvider && !(data.network_config?.base_url || "").trim()) {
			toast.error("Base URL is required for custom providers.");
			return;
		}
		// Create updated provider configuration
		const updatedProvider: ModelProvider = {
			...provider,
			network_config: {
				...provider.network_config,
				base_url: data.network_config?.base_url || undefined,
				default_request_timeout_in_seconds: data.network_config?.default_request_timeout_in_seconds ?? 30,
				max_retries: data.network_config?.max_retries ?? 0,
				// Keep existing values for fields not in the form
				extra_headers: provider.network_config?.extra_headers,
				retry_backoff_initial: provider.network_config?.retry_backoff_initial ?? 1000,
				retry_backoff_max: provider.network_config?.retry_backoff_max ?? 10000,
			},
		};
		updateProvider(updatedProvider)
			.unwrap()
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	useEffect(() => {
		// Reset form with new provider's network_config when provider.name changes
		form.reset({
			network_config: provider.network_config,
		});
	}, [form, provider.name, provider.network_config]);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{showRestartAlert && (
					<Alert>
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							The settings below require a Bifrost service restart to take effect. Current connections will continue with existing settings
							until restart.
						</AlertDescription>
					</Alert>
				)}

				{/* Network Configuration */}
				<div className="space-y-4">
					<div className="grid grid-cols-1 gap-4">
						<FormField
							control={form.control}
							name="network_config.base_url"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Base URL {isCustomProvider ? "(Required for Custom Providers)" : "(Optional)"}</FormLabel>
									<FormControl>
										<Input
											placeholder={isCustomProvider ? "https://api.your-provider.com" : "https://api.example.com"}
											{...field}
											value={field.value || ""}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={form.control}
								name="network_config.default_request_timeout_in_seconds"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>Timeout (seconds)</FormLabel>
										<FormControl>
											<Input placeholder="30" {...field} onChange={(e) => field.onChange(Number(e.target.value))} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.max_retries"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>Max Retries</FormLabel>
										<FormControl>
											<Input placeholder="0" {...field} onChange={(e) => field.onChange(Number(e.target.value))} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 py-2">
					<Button
						type="button"
						variant="outline"
						onClick={() => {
							form.reset({
								network_config: undefined,
							});
							onSubmit(form.getValues());
						}}
						disabled={
							isUpdatingProvider ||
							!provider.network_config ||
							!provider.network_config.base_url ||
							provider.network_config.base_url.trim() === ""
						}
					>
						Remove configuration
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button type="submit" disabled={!form.formState.isDirty || !form.formState.isValid} isLoading={isUpdatingProvider}>
									Save Network Configuration
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
