"use client";

import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider, NetworkConfig } from "@/lib/types/config";
import { betaHeadersFormSchema, type BetaHeadersFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";

// Known beta headers with their prefixes, descriptions, and default support per provider.
// This mirrors the Go ProviderFeatures map in core/providers/anthropic/types.go.
const KNOWN_BETA_HEADERS = [
	{ prefix: "computer-use-", label: "Computer Use", description: "Computer use client tool", defaults: { anthropic: true, vertex: true, bedrock: true, azure: true } },
	{ prefix: "structured-outputs-", label: "Structured Outputs", description: "Strict tool validation and output_format", defaults: { anthropic: true, vertex: false, bedrock: true, azure: true } },
	{ prefix: "advanced-tool-use-", label: "Advanced Tool Use", description: "defer_loading, input_examples, allowed_callers", defaults: { anthropic: true, vertex: false, bedrock: false, azure: true } },
	{ prefix: "mcp-client-", label: "MCP Client", description: "MCP connector support", defaults: { anthropic: true, vertex: false, bedrock: false, azure: true } },
	{ prefix: "prompt-caching-scope-", label: "Prompt Caching Scope", description: "Prompt caching scope control", defaults: { anthropic: true, vertex: false, bedrock: false, azure: true } },
	{ prefix: "compact-", label: "Compaction", description: "Server-side context compaction", defaults: { anthropic: true, vertex: true, bedrock: true, azure: true } },
	{ prefix: "context-management-", label: "Context Management", description: "Context editing (clear_tool_uses, clear_thinking)", defaults: { anthropic: true, vertex: true, bedrock: true, azure: true } },
	{ prefix: "files-api-", label: "Files API", description: "Files API support", defaults: { anthropic: true, vertex: false, bedrock: false, azure: true } },
	{ prefix: "interleaved-thinking-", label: "Interleaved Thinking", description: "Interleaved thinking between tool calls", defaults: { anthropic: true, vertex: true, bedrock: true, azure: true } },
	{ prefix: "skills-", label: "Skills", description: "Agent Skills", defaults: { anthropic: true, vertex: false, bedrock: false, azure: true } },
	{ prefix: "context-1m-", label: "Context 1M", description: "1M context window (beta for Sonnet 4.5/4)", defaults: { anthropic: true, vertex: true, bedrock: true, azure: true } },
	{ prefix: "fast-mode-", label: "Fast Mode", description: "Fast mode (Opus 4.6 research preview)", defaults: { anthropic: true, vertex: false, bedrock: false, azure: false } },
	{ prefix: "redact-thinking-", label: "Redact Thinking", description: "Redact thinking blocks in responses", defaults: { anthropic: true, vertex: false, bedrock: false, azure: true } },
] as const;

const KNOWN_PREFIXES = new Set<string>(KNOWN_BETA_HEADERS.map((h) => h.prefix));

type ProviderKey = "anthropic" | "vertex" | "bedrock" | "azure";

const ANTHROPIC_FAMILY_PROVIDERS: ProviderKey[] = ["anthropic", "vertex", "bedrock", "azure"];

function getProviderKey(providerName: string): ProviderKey | null {
	const name = providerName.toLowerCase();
	if (ANTHROPIC_FAMILY_PROVIDERS.includes(name as ProviderKey)) {
		return name as ProviderKey;
	}
	return null;
}

interface BetaHeadersFormFragmentProps {
	provider: ModelProvider;
}

export function BetaHeadersFormFragment({ provider }: BetaHeadersFormFragmentProps) {
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const providerKey = getProviderKey(provider.name);
	const [newPrefix, setNewPrefix] = useState("");
	const [newPrefixError, setNewPrefixError] = useState<string | null>(null);

	const form = useForm<BetaHeadersFormSchema, any, BetaHeadersFormSchema>({
		resolver: zodResolver(betaHeadersFormSchema) as Resolver<BetaHeadersFormSchema, any, BetaHeadersFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			beta_header_overrides: provider.network_config?.beta_header_overrides ?? {},
		},
	});

	useEffect(() => {
		form.reset({
			beta_header_overrides: provider.network_config?.beta_header_overrides ?? {},
		});
	}, [form, provider.name, provider.network_config?.beta_header_overrides]);

	const overrides = form.watch("beta_header_overrides") ?? {};

	// Manual dirty tracking — RHF's deep equality on records is unreliable with setValue
	const savedOverrides = provider.network_config?.beta_header_overrides ?? {};
	const isManuallyDirty = useMemo(() => {
		const currentKeys = Object.keys(overrides);
		const savedKeys = Object.keys(savedOverrides);
		if (currentKeys.length !== savedKeys.length) return true;
		return currentKeys.some((key) => overrides[key] !== savedOverrides[key]);
	}, [overrides, savedOverrides]);

	useEffect(() => {
		dispatch(setProviderFormDirtyState(isManuallyDirty));
	}, [isManuallyDirty, dispatch]);

	// Custom prefixes are overrides that don't match any known prefix
	const customPrefixes = useMemo(() => {
		return Object.keys(overrides).filter((prefix) => !KNOWN_PREFIXES.has(prefix));
	}, [overrides]);

	const headerRows = useMemo(() => {
		if (!providerKey) return [];
		return KNOWN_BETA_HEADERS.map((header) => {
			const defaultSupported = header.defaults[providerKey];
			const override = overrides[header.prefix];
			return { ...header, defaultSupported, override };
		});
	}, [providerKey, overrides]);

	const onSubmit = (data: BetaHeadersFormSchema) => {
		const cleanedOverrides: Record<string, boolean> = {};
		if (data.beta_header_overrides) {
			for (const [prefix, value] of Object.entries(data.beta_header_overrides)) {
				cleanedOverrides[prefix] = value;
			}
		}

		const updatedProvider: ModelProvider = {
			...provider,
			network_config: {
				...(provider.network_config ?? {} as NetworkConfig),
				beta_header_overrides: Object.keys(cleanedOverrides).length > 0 ? cleanedOverrides : undefined,
			},
		};
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success("Beta header configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update beta header configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	const setOverride = useCallback(
		(prefix: string, value: "default" | "enabled" | "disabled") => {
			const current = form.getValues("beta_header_overrides") ?? {};
			const updated = { ...current };
			if (value === "default") {
				delete updated[prefix];
			} else {
				updated[prefix] = value === "enabled";
			}
			form.setValue("beta_header_overrides", updated, { shouldDirty: true });
		},
		[form],
	);

	const removeCustomPrefix = useCallback(
		(prefix: string) => {
			const current = form.getValues("beta_header_overrides") ?? {};
			const updated = { ...current };
			delete updated[prefix];
			form.setValue("beta_header_overrides", updated, { shouldDirty: true });
		},
		[form],
	);

	const addCustomPrefix = useCallback(() => {
		let prefix = newPrefix.trim().toLowerCase();
		if (!prefix) return;

		// Ensure prefix ends with "-"
		if (!prefix.endsWith("-")) {
			prefix = prefix + "-";
		}

		// Validate
		if (KNOWN_PREFIXES.has(prefix)) {
			setNewPrefixError("This is a known header — use the override dropdown above instead");
			return;
		}
		if (overrides[prefix] !== undefined) {
			setNewPrefixError("This prefix already exists");
			return;
		}
		if (!/^[a-z0-9-]+$/.test(prefix)) {
			setNewPrefixError("Prefix must contain only lowercase letters, numbers, and hyphens");
			return;
		}

		const current = form.getValues("beta_header_overrides") ?? {};
		form.setValue("beta_header_overrides", { ...current, [prefix]: true }, { shouldDirty: true });
		setNewPrefix("");
		setNewPrefixError(null);
	}, [newPrefix, overrides, form]);

	const getSelectValue = (prefix: string): string => {
		const override = overrides[prefix];
		if (override === undefined) return "default";
		return override ? "enabled" : "disabled";
	};

	if (!providerKey) return null;

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6" data-testid="provider-config-beta-headers-content">
				<div className="space-y-2">
					<p className="text-muted-foreground text-xs">
						Configure which Anthropic beta headers are allowed for this provider. Override the defaults when a provider adds or removes support for a beta feature.
					</p>
					<div className="rounded-md border">
						<table className="w-full text-sm">
							<thead>
								<tr className="border-b">
									<th className="px-3 py-2 text-left font-medium">Beta Header</th>
									<th className="px-3 py-2 text-left font-medium">Default</th>
									<th className="w-[180px] px-3 py-2 text-left font-medium">Override</th>
								</tr>
							</thead>
							<tbody>
								{headerRows.map((row) => (
									<tr key={row.prefix} className="border-b last:border-b-0">
										<td className="px-3 py-2">
											<div className="flex flex-col gap-0.5">
												<span className="font-mono text-xs">{row.prefix}*</span>
												<span className="text-muted-foreground text-xs">{row.description}</span>
											</div>
										</td>
										<td className="px-3 py-2">
											<Badge variant={row.defaultSupported ? "default" : "secondary"} className="text-xs">
												{row.defaultSupported ? "Supported" : "Unsupported"}
											</Badge>
										</td>
										<td className="w-[180px] px-3 py-2">
											<Select
												value={getSelectValue(row.prefix)}
												onValueChange={(val) => setOverride(row.prefix, val as "default" | "enabled" | "disabled")}
												disabled={!hasUpdateProviderAccess}
											>
												<SelectTrigger className="h-8 text-xs" data-testid={`provider-beta-override-select-${row.prefix.replace(/-/g, "")}`}>
													<SelectValue />
												</SelectTrigger>
												<SelectContent>
													<SelectItem value="default">Default</SelectItem>
													<SelectItem value="enabled">Supported</SelectItem>
													<SelectItem value="disabled">Unsupported</SelectItem>
												</SelectContent>
											</Select>
										</td>
									</tr>
								))}
								{customPrefixes.map((prefix) => (
									<tr key={prefix} className="border-b last:border-b-0">
										<td className="px-3 py-2">
											<div className="flex flex-col gap-0.5">
												<span className="font-mono text-xs">{prefix}*</span>
												<span className="text-muted-foreground text-xs">Custom header</span>
											</div>
										</td>
										<td className="px-3 py-2">
											<Badge variant="outline" className="text-xs">
												Custom
											</Badge>
										</td>
										<td className="w-[180px] px-3 py-2">
											<div className="flex items-center gap-1">
												<Select
													value={overrides[prefix] ? "enabled" : "disabled"}
													onValueChange={(val) => setOverride(prefix, val as "enabled" | "disabled")}
													disabled={!hasUpdateProviderAccess}
												>
													<SelectTrigger className="h-8 text-xs" data-testid={`provider-beta-custom-override-select-${prefix.replace(/-/g, "")}`}>
														<SelectValue />
													</SelectTrigger>
													<SelectContent>
														<SelectItem value="enabled">Supported</SelectItem>
														<SelectItem value="disabled">Unsupported</SelectItem>
													</SelectContent>
												</Select>
												<Button
													type="button"
													variant="ghost"
													size="icon"
													className="h-8 w-8 shrink-0"
													disabled={!hasUpdateProviderAccess}
													onClick={() => removeCustomPrefix(prefix)}
													data-testid={`provider-beta-remove-prefix-btn-${prefix.replace(/-/g, "")}`}
													aria-label={`Remove custom prefix ${prefix}`}
												>
													<Trash2 className="h-3.5 w-3.5" />
												</Button>
											</div>
										</td>
									</tr>
								))}
							</tbody>
						</table>
					</div>

					<div className="flex items-start gap-2 pt-2">
						<div className="flex-1">
							<Input
								placeholder="Add custom beta header prefix (e.g. new-feature-)"
								value={newPrefix}
								onChange={(e) => {
									setNewPrefix(e.target.value);
									setNewPrefixError(null);
								}}
								onKeyDown={(e) => {
									if (e.key === "Enter") {
										e.preventDefault();
										addCustomPrefix();
									}
								}}
								disabled={!hasUpdateProviderAccess}
								className="h-8 text-xs"
								data-testid="provider-beta-custom-prefix-input"
								aria-label="Custom beta header prefix"
								aria-describedby={newPrefixError ? "custom-prefix-error" : undefined}
							/>
							{newPrefixError && <p className="text-destructive mt-1 text-xs" id="custom-prefix-error">{newPrefixError}</p>}
						</div>
						<Button 
							type="button" 
							variant="outline" 
							size="sm" 
							className="h-8" 
							disabled={!hasUpdateProviderAccess || !newPrefix.trim()} 
							onClick={addCustomPrefix}
							data-testid="provider-beta-add-prefix-btn"
						>
							<Plus className="mr-1 h-3.5 w-3.5" />
							Add
						</Button>
					</div>
				</div>

				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="submit"
						disabled={!isManuallyDirty || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
						data-testid="provider-beta-save-btn"
					>
						Save Beta Header Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}
