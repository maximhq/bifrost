import { buildProviderUpdatePayload } from "@/app/workspace/providers/views/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
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
	{
		prefix: "computer-use-",
		label: "计算机使用",
		description: "计算机使用客户端工具",
		defaults: { anthropic: true, vertex: true, bedrock: true, bedrock_mantle: true, azure: true },
	},
	{
		prefix: "structured-outputs-",
		label: "结构化输出",
		description: "严格工具验证和 output_format",
		defaults: { anthropic: true, vertex: false, bedrock: true, bedrock_mantle: true, azure: true },
	},
	{
		prefix: "advanced-tool-use-",
		label: "高级工具使用",
		description: "defer_loading, input_examples, allowed_callers",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: true },
	},
	{
		prefix: "mcp-client-",
		label: "MCP 客户端",
		description: "MCP 连接器支持",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: true },
	},
	{
		prefix: "prompt-caching-scope-",
		label: "提示缓存范围",
		description: "提示缓存范围控制",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: true },
	},
	{
		prefix: "compact-",
		label: "压缩",
		description: "服务端上下文压缩",
		defaults: { anthropic: true, vertex: true, bedrock: true, bedrock_mantle: true, azure: true },
	},
	{
		prefix: "context-management-",
		label: "上下文管理",
		description: "上下文编辑（clear_tool_uses、clear_thinking）",
		defaults: { anthropic: true, vertex: true, bedrock: true, bedrock_mantle: true, azure: true },
	},
	{
		prefix: "files-api-",
		label: "Files API",
		description: "Files API 支持",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: true },
	},
	{
		prefix: "interleaved-thinking-",
		label: "交错思考",
		description: "工具调用之间的交错思考",
		defaults: { anthropic: true, vertex: true, bedrock: true, bedrock_mantle: true, azure: true },
	},
	{
		prefix: "skills-",
		label: "技能",
		description: "Agent 技能",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: true },
	},
	{
		prefix: "context-1m-",
		label: "上下文 1M",
		description: "1M 上下文窗口（Sonnet 4.5/4 测试版）",
		defaults: { anthropic: true, vertex: true, bedrock: true, bedrock_mantle: true, azure: true },
	},
	{
		prefix: "fast-mode-",
		label: "快速模式",
		description: "快速模式（Opus 4.6 研究预览）",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: false },
	},
	{
		prefix: "redact-thinking-",
		label: "隐藏思考内容",
		description: "隐藏响应中的思考块",
		defaults: { anthropic: true, vertex: false, bedrock: false, bedrock_mantle: false, azure: true },
	},
] as const;

const KNOWN_PREFIXES = new Set<string>(KNOWN_BETA_HEADERS.map((h) => h.prefix));

type ProviderKey = "anthropic" | "vertex" | "bedrock" | "bedrock_mantle" | "azure";

const ANTHROPIC_FAMILY_PROVIDERS: ProviderKey[] = ["anthropic", "vertex", "bedrock", "bedrock_mantle", "azure"];

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

		updateProvider(
			buildProviderUpdatePayload(provider, {
				network_config: {
					...(provider.network_config ?? ({} as NetworkConfig)),
					beta_header_overrides: Object.keys(cleanedOverrides).length > 0 ? cleanedOverrides : undefined,
				},
			}),
		)
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
			setNewPrefixError("This is a known header; use the override dropdown above instead");
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
			<form onSubmit={form.handleSubmit(onSubmit)} data-testid="provider-config-beta-headers-content">
				<div className="space-y-2 px-6 pb-6">
					<p className="text-muted-foreground text-xs">
						Configure which Anthropic beta headers are allowed for this provider. Override the defaults when a provider adds or removes
						support for a beta feature.
					</p>
					<div className="rounded-md border">
						<table className="w-full text-sm">
							<thead>
								<tr className="border-b">
									<th className="px-3 py-2 text-left font-medium">测试版请求头</th>
									<th className="px-3 py-2 text-left font-medium">默认</th>
									<th className="w-[180px] px-3 py-2 text-left font-medium">覆盖</th>
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
												{row.defaultSupported ? "支持" : "不支持"}
											</Badge>
										</td>
										<td className="w-[180px] px-3 py-2">
											<Select
												value={getSelectValue(row.prefix)}
												onValueChange={(val) => setOverride(row.prefix, val as "default" | "enabled" | "disabled")}
												disabled={!hasUpdateProviderAccess}
											>
												<SelectTrigger
													className="h-8 text-xs"
													data-testid={`provider-beta-override-select-${row.prefix.replace(/-/g, "")}`}
												>
													<SelectValue />
												</SelectTrigger>
												<SelectContent>
													<SelectItem value="default">默认</SelectItem>
													<SelectItem value="enabled">支持</SelectItem>
													<SelectItem value="disabled">不支持</SelectItem>
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
												<span className="text-muted-foreground text-xs">自定义请求头</span>
											</div>
										</td>
										<td className="px-3 py-2">
											<Badge variant="outline" className="text-xs">自定义</Badge>
										</td>
										<td className="w-[180px] px-3 py-2">
											<div className="flex items-center gap-1">
												<Select
													value={overrides[prefix] ? "enabled" : "disabled"}
													onValueChange={(val) => setOverride(prefix, val as "enabled" | "disabled")}
													disabled={!hasUpdateProviderAccess}
												>
													<SelectTrigger
														className="h-8 text-xs"
														data-testid={`provider-beta-custom-override-select-${prefix.replace(/-/g, "")}`}
													>
														<SelectValue />
													</SelectTrigger>
													<SelectContent>
														<SelectItem value="enabled">支持</SelectItem>
														<SelectItem value="disabled">不支持</SelectItem>
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
								placeholder="添加自定义测试版请求头前缀（例如 new-feature-）"
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
								aria-label="自定义测试版请求头前缀"
								aria-describedby={newPrefixError ? "custom-prefix-error" : undefined}
							/>
							{newPrefixError && (
								<p className="text-destructive mt-1 text-xs" id="custom-prefix-error">
									{newPrefixError}
								</p>
							)}
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
							<Plus className="mr-1 h-3.5 w-3.5" />添加</Button>
					</div>
				</div>

				<div className="bg-card sticky bottom-0 flex justify-end gap-2 rounded-b-sm border-t px-6 py-4">
					<Button
						type="submit"
						disabled={!isManuallyDirty || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
						data-testid="provider-beta-save-btn"
					>保存测试版请求头配置</Button>
				</div>
			</form>
		</Form>
	);
}