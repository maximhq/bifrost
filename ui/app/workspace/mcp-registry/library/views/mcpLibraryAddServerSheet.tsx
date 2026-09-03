import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useT } from "@/lib/i18n";
import { getErrorMessage, useCreateMCPLibraryEntryMutation } from "@/lib/store";
import type { CreateMCPLibraryEntryRequest, MCPAuthType, MCPConnectionType } from "@/lib/types/mcp";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { Info } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import {
	authKindOf,
	authScopeOf,
	type MCPAuthKind,
	type MCPAuthScope,
	SectionHeader,
	StdioRuntimeNotice,
} from "../../views/mcpClientFormFields";

interface MCPLibraryAddServerFormData {
	name: string;
	description: string;
	category: string;
	publisher: string;
	connection_type: MCPConnectionType;
	connection_url: string;
	command: string;
	args: string;
	envs: string;
	auth_type: MCPAuthType;
	required_header_keys: string;
	icon_url: string;
	docs_url: string;
	tags: string;
}

interface MCPLibraryAddServerSheetProps {
	open: boolean;
	onClose: () => void;
}

const DEFAULTS: MCPLibraryAddServerFormData = {
	name: "",
	description: "",
	category: "",
	publisher: "",
	connection_type: "http",
	connection_url: "",
	command: "",
	args: "",
	envs: "",
	auth_type: "none",
	required_header_keys: "",
	icon_url: "",
	docs_url: "",
	tags: "",
};

// Split a comma/newline-separated string into a trimmed, non-empty list.
function parseList(text: string): string[] {
	return text
		.split(/[\n,]/)
		.map((s) => s.trim())
		.filter(Boolean);
}

/**
 * Publishes a library listing: a reusable description of an MCP server that
 * members can later install. Deliberately mirrors the layout and auth
 * vocabulary of the create-server sheet, but collects no credentials: those
 * are supplied per install, by whoever installs the entry.
 */
export function MCPLibraryAddServerSheet({ open, onClose }: MCPLibraryAddServerSheetProps) {
	const t = useT();
	const [createEntry, { isLoading }] = useCreateMCPLibraryEntryMutation();

	// Token exchange is backed by the deployment's identity-provider
	// integration, so the option only renders when one is enabled, same gate
	// as the create-server sheet.
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, { skip: !IS_ENTERPRISE });
	const idpConfigured = !!scimProviders?.find((p) => (p as { enabled?: boolean }).enabled);

	const methods = useForm<MCPLibraryAddServerFormData>({ defaultValues: DEFAULTS });
	const { control, handleSubmit, watch, setValue, reset } = methods;

	useEffect(() => {
		if (open) reset(DEFAULTS);
	}, [open, reset]);

	const connectionType = watch("connection_type");
	const authType = watch("auth_type");
	const isStdio = connectionType === "stdio";
	const authKind = authKindOf(authType);
	const authScope = authScopeOf(authType);
	const needsHeaderKeys = authKind === "headers";

	const applyAuthKind = (kind: MCPAuthKind) => {
		if (kind === "none" || kind === "token_exchange") {
			setValue("auth_type", kind);
			return;
		}
		if (kind === "oauth") {
			setValue("auth_type", authScope === "per_user" ? "per_user_oauth" : "oauth");
			return;
		}
		setValue("auth_type", authScope === "per_user" ? "per_user_headers" : "headers");
	};

	const applyAuthScope = (scope: MCPAuthScope) => {
		if (authKind === "oauth") {
			setValue("auth_type", scope === "per_user" ? "per_user_oauth" : "oauth");
		} else if (authKind === "headers") {
			setValue("auth_type", scope === "per_user" ? "per_user_headers" : "headers");
		}
	};

	const onSubmit = async (data: MCPLibraryAddServerFormData) => {
		const tags = parseList(data.tags);
		const payload: CreateMCPLibraryEntryRequest = {
			name: data.name.trim(),
			description: data.description.trim() || undefined,
			category: data.category.trim() || undefined,
			publisher: data.publisher.trim() || undefined,
			connection_type: data.connection_type,
			auth_type: data.auth_type,
			icon_url: data.icon_url.trim() || undefined,
			docs_url: data.docs_url.trim() || undefined,
			tags: tags.length ? tags : undefined,
		};

		if (isStdio) {
			payload.stdio_config = {
				command: data.command.trim(),
				args: parseList(data.args),
				envs: parseList(data.envs),
			};
		} else {
			payload.connection_url = data.connection_url.trim();
		}

		if (needsHeaderKeys) {
			payload.required_header_keys = parseList(data.required_header_keys);
		}

		try {
			await createEntry(payload).unwrap();
			toast.success(t("mcp.library.addedToast"));
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && onClose()}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky px-4 md:px-8 -top-4 bg-card z-10">
					<SheetTitle>{t("mcp.library.addTitle")}</SheetTitle>
					<SheetDescription>{t("mcp.library.addDesc")}</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							<div className="rounded-lg border border-blue-200 bg-blue-50 p-3" data-testid="mcp-add-listing-notice">
								<div className="flex items-start gap-2">
									<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-700" />
									<p className="text-xs text-blue-900">{t("mcp.library.addNotice")}</p>
								</div>
							</div>

							{/* Listing details */}
							<div className="space-y-4">
								<SectionHeader title={t("mcp.library.listingDetails")} description={t("mcp.library.listingDetailsDesc")} />
								<div className="space-y-4 rounded-md border p-4">
									<FormField
										control={control}
										name="name"
										rules={{
											required: t("mcp.validation.listingNameRequired"),
											validate: (v) => v.trim().length > 0 || t("mcp.validation.listingNameRequired"),
										}}
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("common.name")}</FormLabel>
												<FormControl>
													<Input {...field} placeholder={t("mcp.library.namePlaceholder")} data-testid="mcp-add-name-input" />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="description"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("common.description")}</FormLabel>
												<FormControl>
													<Textarea
														{...field}
														placeholder={t("mcp.library.descriptionPlaceholder")}
														data-testid="mcp-add-description-input"
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
							</div>

							<DottedSeparator />

							{/* Connection */}
							<div className="space-y-4">
								<SectionHeader title={t("mcp.library.connection")} description={t("mcp.library.connectionDesc")} />
								<div className="space-y-4 rounded-md border p-4">
									<FormField
										control={control}
										name="connection_type"
										render={({ field }) => (
											<FormItem className="w-full">
												<FormLabel>{t("mcp.connectionType")}</FormLabel>
												<Select
													value={field.value}
													onValueChange={(value: MCPConnectionType) => {
														field.onChange(value);
														// STDIO servers are launched locally, so none of the
														// network auth types apply to them.
														if (value === "stdio") setValue("auth_type", "none");
													}}
												>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="mcp-add-connection-type">
															<SelectValue />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="http">{t("mcp.form.httpStreamable")}</SelectItem>
														<SelectItem value="sse">{t("mcp.form.sse")}</SelectItem>
														<SelectItem value="stdio">{t("mcp.form.stdio")}</SelectItem>
													</SelectContent>
												</Select>
												<FormMessage />
											</FormItem>
										)}
									/>

									{!isStdio && (
										<FormField
											control={control}
											name="connection_url"
											rules={{
												validate: (v) => isStdio || v.trim().length > 0 || t("mcp.validation.connectionUrlRequired"),
											}}
											render={({ field }) => (
												<FormItem>
													<FormLabel>{t("mcp.form.connectionUrl")}</FormLabel>
													<FormControl>
														<Input {...field} placeholder={t("mcp.library.urlPlaceholder")} data-testid="mcp-add-url-input" />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
									)}

									{isStdio && (
										<>
											<StdioRuntimeNotice />
											<FormField
												control={control}
												name="command"
												rules={{
													validate: (v) => !isStdio || v.trim().length > 0 || t("mcp.validation.commandRequiredStdio"),
												}}
												render={({ field }) => (
													<FormItem>
														<FormLabel>{t("mcp.form.command")}</FormLabel>
														<FormControl>
															<Input {...field} placeholder={t("mcp.library.commandPlaceholder")} data-testid="mcp-add-command-input" />
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
											<FormField
												control={control}
												name="args"
												render={({ field }) => (
													<FormItem>
														<FormLabel>{t("mcp.form.arguments")}</FormLabel>
														<FormControl>
															<Input {...field} placeholder={t("mcp.library.argsPlaceholder")} data-testid="mcp-add-args-input" />
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
											<FormField
												control={control}
												name="envs"
												render={({ field }) => (
													<FormItem>
														<FormLabel>{t("mcp.library.envNames")}</FormLabel>
														<FormControl>
															<Input {...field} placeholder={t("mcp.library.envNamesPlaceholder")} data-testid="mcp-add-envs-input" />
														</FormControl>
														<p className="text-muted-foreground text-xs">{t("mcp.library.namesOnly")}</p>
														<FormMessage />
													</FormItem>
												)}
											/>
										</>
									)}
								</div>
							</div>

							{!isStdio && (
								<>
									<DottedSeparator />

									{/* Authentication */}
									<div className="space-y-4">
										<SectionHeader title={t("mcp.library.authentication")} description={t("mcp.library.authenticationDesc")} />
										<div className="space-y-4 rounded-md border p-4">
											<FormItem className="w-full">
												<FormLabel>{t("mcp.form.authType")}</FormLabel>
												<Select value={authKind} onValueChange={(value: MCPAuthKind) => applyAuthKind(value)}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="mcp-add-auth-type">
															<SelectValue />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="none">{t("mcp.form.authNone")}</SelectItem>
														<SelectItem value="headers">{t("mcp.form.authHeaders")}</SelectItem>
														<SelectItem value="oauth">{t("mcp.form.authOauth")}</SelectItem>
														{IS_ENTERPRISE && idpConfigured && (
															<SelectItem value="token_exchange">{t("mcp.form.authTokenExchange")}</SelectItem>
														)}
													</SelectContent>
												</Select>
											</FormItem>

											{authKind !== "none" && authKind !== "token_exchange" && (
												<FormItem className="w-full">
													<FormLabel>{t("mcp.form.authScope")}</FormLabel>
													<Select value={authScope} onValueChange={(value: MCPAuthScope) => applyAuthScope(value)}>
														<FormControl>
															<SelectTrigger className="w-full" data-testid="mcp-add-auth-scope">
																<SelectValue />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															<SelectItem value="shared">{t("mcp.form.scopeShared")}</SelectItem>
															<SelectItem value="per_user">{t("mcp.form.scopePerUser")}</SelectItem>
														</SelectContent>
													</Select>
												</FormItem>
											)}

											{needsHeaderKeys && (
												<FormField
													control={control}
													name="required_header_keys"
													render={({ field }) => (
														<FormItem>
															<FormLabel>{t("mcp.library.requiredHeaderNames")}</FormLabel>
															<FormControl>
																<Input
																	{...field}
																	placeholder={t("mcp.library.requiredHeaderNamesPlaceholder")}
																	data-testid="mcp-add-header-keys-input"
																/>
															</FormControl>
															<p className="text-muted-foreground text-xs">{t("mcp.library.namesOnly")}</p>
															<FormMessage />
														</FormItem>
													)}
												/>
											)}
										</div>
									</div>
								</>
							)}

							<DottedSeparator />

							{/* Catalog metadata */}
							<div className="space-y-4">
								<SectionHeader title={t("mcp.library.discovery")} description={t("mcp.library.discoveryDesc")} />
								<div className="space-y-4 rounded-md border p-4">
									<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
										<FormField
											control={control}
											name="category"
											render={({ field }) => (
												<FormItem>
													<FormLabel>{t("mcp.library.category")}</FormLabel>
													<FormControl>
														<Input {...field} placeholder={t("mcp.library.categoryPlaceholder")} data-testid="mcp-add-category-input" />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={control}
											name="publisher"
											render={({ field }) => (
												<FormItem>
													<FormLabel>{t("mcp.library.publisher")}</FormLabel>
													<FormControl>
														<Input {...field} placeholder={t("mcp.library.publisherPlaceholder")} data-testid="mcp-add-publisher-input" />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
									</div>
									<FormField
										control={control}
										name="tags"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("mcp.library.tags")}</FormLabel>
												<FormControl>
													<Input {...field} placeholder={t("mcp.library.tagsPlaceholder")} data-testid="mcp-add-tags-input" />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
										<FormField
											control={control}
											name="icon_url"
											render={({ field }) => (
												<FormItem>
													<FormLabel>{t("mcp.library.iconUrl")}</FormLabel>
													<FormControl>
														<Input {...field} placeholder={t("mcp.library.urlGenericPlaceholder")} data-testid="mcp-add-icon-input" />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={control}
											name="docs_url"
											render={({ field }) => (
												<FormItem>
													<FormLabel>{t("mcp.library.docsUrl")}</FormLabel>
													<FormControl>
														<Input {...field} placeholder={t("mcp.library.urlGenericPlaceholder")} data-testid="mcp-add-docs-input" />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
									</div>
								</div>
							</div>
						</div>

						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="mcp-add-cancel-btn">
								{t("common.cancel")}
							</Button>
							<Button type="submit" disabled={isLoading} isLoading={isLoading} data-testid="mcp-add-submit-btn">
								{t("mcp.library.addToLibrary")}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}