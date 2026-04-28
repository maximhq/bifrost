import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { proxyOnlyFormSchema, type EnvVar, type ProxyOnlyFormSchema } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { toEnvVarFormValue, toOptionalEnvVarPayload } from "@/lib/utils/envVarForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface ProxyFormFragmentProps {
	provider: ModelProvider;
}

export function ProxyFormFragment({ provider }: ProxyFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<ProxyOnlyFormSchema>({
		resolver: zodResolver(proxyOnlyFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			proxy_config: {
				type: provider.proxy_config?.type,
				url: toEnvVarFormValue(provider.proxy_config?.url as EnvVar | string | undefined),
				username: toEnvVarFormValue(provider.proxy_config?.username as EnvVar | string | undefined),
				password: toEnvVarFormValue(provider.proxy_config?.password as EnvVar | string | undefined),
				ca_cert_pem: toEnvVarFormValue(provider.proxy_config?.ca_cert_pem as EnvVar | string | undefined),
			},
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	useEffect(() => {
		form.reset({
			proxy_config: {
				type: provider.proxy_config?.type,
				url: toEnvVarFormValue(provider.proxy_config?.url as EnvVar | string | undefined),
				username: toEnvVarFormValue(provider.proxy_config?.username as EnvVar | string | undefined),
				password: toEnvVarFormValue(provider.proxy_config?.password as EnvVar | string | undefined),
				ca_cert_pem: toEnvVarFormValue(provider.proxy_config?.ca_cert_pem as EnvVar | string | undefined),
			},
		});
	}, [form, provider.name, provider.proxy_config]);

	const watchedProxyType = form.watch("proxy_config.type");

	const onSubmit = (data: ProxyOnlyFormSchema) => {
		updateProvider(
			buildProviderUpdatePayload(provider, {
				proxy_config: {
					type: data.proxy_config?.type ?? "none",
					url: toOptionalEnvVarPayload(data.proxy_config?.url),
					username: toOptionalEnvVarPayload(data.proxy_config?.username),
					password: toOptionalEnvVarPayload(data.proxy_config?.password),
					ca_cert_pem: toOptionalEnvVarPayload(data.proxy_config?.ca_cert_pem),
				},
			}),
		)
			.unwrap()
			.then(() => {
				toast.success("Provider configuration updated successfully");
				form.reset(data);
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
				{/* Proxy Configuration */}
				<div className="space-y-4">
					<div className="space-y-4">
						<FormField
							control={form.control}
							name="proxy_config.type"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Proxy Type</FormLabel>
									<Select
										onValueChange={field.onChange}
										value={field.value === "none" ? "" : field.value}
										disabled={!hasUpdateProviderAccess}
									>
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
												<EnvVarInput
													placeholder="http://proxy.example.com or env.OPENAI_PROXY_URL"
													{...field}
													value={field.value}
													disabled={!hasUpdateProviderAccess}
													data-testid="env-var-proxy-url"
												/>
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
													<EnvVarInput
														placeholder="Proxy username or env.OPENAI_PROXY_USERNAME"
														{...field}
														value={field.value}
														disabled={!hasUpdateProviderAccess}
														data-testid="env-var-proxy-username"
													/>
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
													<EnvVarInput
														type="password"
														placeholder="Proxy password or env.OPENAI_PROXY_PASSWORD"
														hideValueWhenEnv
														redactNonEnvValue
														{...field}
														value={field.value}
														disabled={!hasUpdateProviderAccess}
														data-testid="env-var-proxy-password"
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
								<FormField
									control={form.control}
									name="proxy_config.ca_cert_pem"
									render={({ field }) => (
										<FormItem>
											<FormLabel>CA Certificate (PEM) (Optional)</FormLabel>
											<FormControl>
												<EnvVarInput
													variant="textarea"
													placeholder="-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE----- or env.OPENAI_PROXY_CA_CERT_PEM"
													className="font-mono text-xs"
													rows={6}
													hideValueWhenEnv
													redactNonEnvValue
													{...field}
													value={field.value}
													disabled={!hasUpdateProviderAccess}
													data-testid="env-var-proxy-ca-cert-pem"
												/>
											</FormControl>
											<FormDescription>
												PEM-encoded CA certificate to trust for TLS connections through SSL-intercepting proxies. You can also use
												<code> env.YOUR_PROXY_CA_CERT_VAR</code>.
											</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>
						</div>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2">
					<Button
						type="button"
						variant="outline"
						onClick={() => {
							onSubmit({ proxy_config: { type: "none" } });
						}}
						disabled={!hasUpdateProviderAccess || isUpdatingProvider || !provider.proxy_config || provider.proxy_config.type === "none"}
					>
						Remove configuration
					</Button>
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						Save Proxy Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}