"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { proxyOnlyFormSchema, type ProxyOnlyFormSchema } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { fi } from "zod/v4/locales";

interface ProxyFormFragmentProps {
	provider: ModelProvider;
	showRestartAlert?: boolean;
}

export function ProxyFormFragment({ provider, showRestartAlert = false }: ProxyFormFragmentProps) {
	const dispatch = useAppDispatch();
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<ProxyOnlyFormSchema>({
		resolver: zodResolver(proxyOnlyFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			proxy_config: {
				type: provider.proxy_config?.type,
				url: provider.proxy_config?.url || "",
				username: provider.proxy_config?.username || "",
				password: provider.proxy_config?.password || "",
			},
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty]);

	useEffect(() => {
		form.reset({
			proxy_config: {
				type: provider.proxy_config?.type,
				url: provider.proxy_config?.url || "",
				username: provider.proxy_config?.username || "",
				password: provider.proxy_config?.password || "",
			},
		});
	}, [form, provider.name, provider.proxy_config]);

	const watchedProxyType = form.watch("proxy_config.type");

	const onSubmit = (data: ProxyOnlyFormSchema) => {
		updateProvider({
			...provider,
			proxy_config: {
				type: data.proxy_config?.type ?? "none",
				url: data.proxy_config?.url || undefined,
				username: data.proxy_config?.username || undefined,
				password: data.proxy_config?.password || undefined,
			},
		})
			.unwrap()
			.then(() => {
				toast.success("Provider configuration updated successfully");
			})
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{showRestartAlert && form.formState.isDirty && (
					<Alert>
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							The settings below require a Bifrost service restart to take effect. Current connections will continue with existing settings
							until restart.
						</AlertDescription>
					</Alert>
				)}

				{/* Proxy Configuration */}
				<div className="space-y-4">
					<div className="space-y-4">
						<FormField
							control={form.control}
							name="proxy_config.type"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Proxy Type</FormLabel>
									<Select onValueChange={field.onChange} value={field.value === "none" ? "" : field.value}>
										<FormControl>
											<SelectTrigger className="w-48">
												<SelectValue placeholder="Select type" />
											</SelectTrigger>
										</FormControl>
										<SelectContent>
											<SelectItem value="http">HTTP</SelectItem>
											<SelectItem value="socks5">SOCKS5</SelectItem>
											<SelectItem value="environment">Environment</SelectItem>
										</SelectContent>
									</Select>
									<FormMessage />
								</FormItem>
							)}
						/>

						<div
							className={cn(
								"block transition-all duration-200",
								(!watchedProxyType || watchedProxyType === "none" || watchedProxyType === "environment") && "hidden",
							)}
						>
							<div className="space-y-4 pt-2">
								<FormField
									control={form.control}
									name="proxy_config.url"
									render={({ field }) => (
										<FormItem>
											<FormLabel>Proxy URL</FormLabel>
											<FormControl>
												<Input placeholder="http://proxy.example.com" {...field} value={field.value || ""} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
								<div className="grid grid-cols-2 gap-4">
									<FormField
										control={form.control}
										name="proxy_config.username"
										render={({ field }) => (
											<FormItem>
												<FormLabel>Username</FormLabel>
												<FormControl>
													<Input placeholder="Proxy username" {...field} value={field.value || ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={form.control}
										name="proxy_config.password"
										render={({ field }) => (
											<FormItem>
												<FormLabel>Password</FormLabel>
												<FormControl>
													<Input type="password" placeholder="Proxy password" {...field} value={field.value || ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
							</div>
						</div>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="button"
						variant="outline"
						onClick={() => {
							onSubmit({ proxy_config: { type: "none", url: "" } });
						}}
						disabled={isUpdatingProvider || !provider.proxy_config || provider.proxy_config.type === "none"}
					>
						Remove configuration
					</Button>
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !form.formState.isValid || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Proxy Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}
