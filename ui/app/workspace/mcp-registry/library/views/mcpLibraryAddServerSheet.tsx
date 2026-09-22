import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { IS_ENTERPRISE } from "@/lib/constants/config";
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
			toast.success("Server added to the library.");
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && onClose()}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky px-4 md:px-8 -top-4 bg-card z-10">
					<SheetTitle>Publish Server to Library</SheetTitle>
					<SheetDescription>
						Add a reusable server listing that members can discover and install. Nothing connects to Bifrost until someone installs it.
					</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							<div className="rounded-lg border border-blue-200 bg-blue-50 p-3" data-testid="mcp-add-listing-notice">
								<div className="flex items-start gap-2">
									<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-700" />
									<p className="text-xs text-blue-900">
										This listing stays in your organization&apos;s library. It is not published to the global Bifrost MCP catalog. It is
										also a catalog entry, not a live MCP connection: credentials are never stored on the listing, each person who installs
										it supplies their own.
									</p>
								</div>
							</div>

							{/* Listing details */}
							<div className="space-y-4">
								<SectionHeader title="Listing Details" description="How this server appears to members browsing the library." />
								<div className="space-y-4 rounded-md border p-4">
									<FormField
										control={control}
										name="name"
										rules={{
											required: "Name is required",
											validate: (v) => v.trim().length > 0 || "Name is required",
										}}
										render={({ field }) => (
											<FormItem>
												<FormLabel>Name</FormLabel>
												<FormControl>
													<Input {...field} placeholder="My Internal Server" data-testid="mcp-add-name-input" />
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
												<FormLabel>Description</FormLabel>
												<FormControl>
													<Textarea {...field} placeholder="What this server does..." data-testid="mcp-add-description-input" />
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
								<SectionHeader title="Connection" description="How Bifrost will reach this server once a member installs the listing." />
								<div className="space-y-4 rounded-md border p-4">
									<FormField
										control={control}
										name="connection_type"
										render={({ field }) => (
											<FormItem className="w-full">
												<FormLabel>Connection Type</FormLabel>
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
														<SelectItem value="http">HTTP (Streamable)</SelectItem>
														<SelectItem value="sse">Server-Sent Events (SSE)</SelectItem>
														<SelectItem value="stdio">STDIO</SelectItem>
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
												validate: (v) => isStdio || v.trim().length > 0 || "Connection URL is required",
											}}
											render={({ field }) => (
												<FormItem>
													<FormLabel>Connection URL</FormLabel>
													<FormControl>
														<Input {...field} placeholder="https://my-server.internal/mcp" data-testid="mcp-add-url-input" />
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
													validate: (v) => !isStdio || v.trim().length > 0 || "Command is required for stdio",
												}}
												render={({ field }) => (
													<FormItem>
														<FormLabel>Command</FormLabel>
														<FormControl>
															<Input {...field} placeholder="npx" data-testid="mcp-add-command-input" />
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
														<FormLabel>Arguments (comma-separated)</FormLabel>
														<FormControl>
															<Input {...field} placeholder="-y, my-package" data-testid="mcp-add-args-input" />
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
														<FormLabel>Environment Variable Names (comma-separated)</FormLabel>
														<FormControl>
															<Input {...field} placeholder="API_KEY, DB_URL" data-testid="mcp-add-envs-input" />
														</FormControl>
														<p className="text-muted-foreground text-xs">Names only; whoever installs the listing supplies the values.</p>
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
										<SectionHeader
											title="Authentication"
											description="The scheme this server expects. It prefills the install form; no secrets are stored on the listing."
										/>
										<div className="space-y-4 rounded-md border p-4">
											<FormItem className="w-full">
												<FormLabel>Authentication Type</FormLabel>
												<Select value={authKind} onValueChange={(value: MCPAuthKind) => applyAuthKind(value)}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="mcp-add-auth-type">
															<SelectValue />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="none">None</SelectItem>
														<SelectItem value="headers">Headers</SelectItem>
														<SelectItem value="oauth">OAuth 2.0</SelectItem>
														{IS_ENTERPRISE && idpConfigured && (
															<SelectItem value="token_exchange">Token Exchange (On-Behalf-Of)</SelectItem>
														)}
													</SelectContent>
												</Select>
											</FormItem>

											{authKind !== "none" && authKind !== "token_exchange" && (
												<FormItem className="w-full">
													<FormLabel>Auth Scope</FormLabel>
													<Select value={authScope} onValueChange={(value: MCPAuthScope) => applyAuthScope(value)}>
														<FormControl>
															<SelectTrigger className="w-full" data-testid="mcp-add-auth-scope">
																<SelectValue />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															<SelectItem value="shared">Shared</SelectItem>
															<SelectItem value="per_user">Per-User</SelectItem>
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
															<FormLabel>Required Header Names (comma-separated)</FormLabel>
															<FormControl>
																<Input {...field} placeholder="X-Api-Key, X-Tenant-ID" data-testid="mcp-add-header-keys-input" />
															</FormControl>
															<p className="text-muted-foreground text-xs">Names only; whoever installs the listing supplies the values.</p>
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
								<SectionHeader title="Discovery" description="Optional metadata used to browse, filter, and attribute this listing." />
								<div className="space-y-4 rounded-md border p-4">
									<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
										<FormField
											control={control}
											name="category"
											render={({ field }) => (
												<FormItem>
													<FormLabel>Category</FormLabel>
													<FormControl>
														<Input {...field} placeholder="e.g. Database" data-testid="mcp-add-category-input" />
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
													<FormLabel>Publisher</FormLabel>
													<FormControl>
														<Input {...field} placeholder="e.g. Platform Team" data-testid="mcp-add-publisher-input" />
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
												<FormLabel>Tags (comma-separated)</FormLabel>
												<FormControl>
													<Input {...field} placeholder="internal, database" data-testid="mcp-add-tags-input" />
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
													<FormLabel>Icon URL</FormLabel>
													<FormControl>
														<Input {...field} placeholder="https://..." data-testid="mcp-add-icon-input" />
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
													<FormLabel>Docs URL</FormLabel>
													<FormControl>
														<Input {...field} placeholder="https://..." data-testid="mcp-add-docs-input" />
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
								Cancel
							</Button>
							<Button type="submit" disabled={isLoading} isLoading={isLoading} data-testid="mcp-add-submit-btn">
								Add to Library
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}