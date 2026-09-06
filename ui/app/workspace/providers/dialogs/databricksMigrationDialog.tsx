"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import {
	getErrorMessage,
	providersApi,
	useAppDispatch,
	useAppSelector,
	useCreateProviderKeyMutation,
	useCreateProviderMutation,
	useDeleteProviderKeyMutation,
	useDeleteProviderMutation,
	useLazyGetProviderKeysQuery,
	useLazyGetProviderQuery,
	useRefreshProviderModelsMutation,
	useUpdateProviderKeyMutation,
	useUpdateProviderMutation,
} from "@/lib/store";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import {
	buildDatabricksMigrationPlan,
	buildMigrationSteps,
	DATABRICKS_PROVIDER,
	DatabricksApiFormat,
	ExistingTarget,
	getErrorStatus,
	isSecretVarSet,
	maskSecret,
	MigrationApi,
	normalizeWorkspaceUrl,
	MigrationPlan,
	MigrationResult,
	MigrationStep,
	planNeedsInput,
	runDatabricksMigration,
} from "@/lib/utils/databricksMigration";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, Check, CircleDashed, Loader2, X } from "lucide-react";
import { ReactNode, useEffect, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";

interface Props {
	show: boolean;
	provider: ModelProvider;
	onDeferred: () => void;
	onMigrated: () => void;
}

type Stage = "intro" | "loading" | "preview" | "running" | "finished";

const API_FORMAT_KEYS: Record<DatabricksApiFormat, string> = {
	auto: "providers.databricksMigration.apiAuto",
	model_serving: "providers.databricksMigration.apiModelServing",
	ai_gateway: "providers.databricksMigration.apiAiGateway",
};

/** Wraps an RTK error so the pure orchestrator can read a message and HTTP status. */
const wrapError = (err: unknown): Error =>
	Object.assign(new Error(getErrorMessage(err)), {
		status: getErrorStatus(err),
	});

export default function DatabricksMigrationDialog({ show, provider, onDeferred, onMigrated }: Props) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const dispatch = useAppDispatch();
	const providerFormIsDirty = useAppSelector((state) => state.provider.isDirty);
	const canCreate = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);
	const hasAccess = canCreate && canUpdate && canDelete;

	const [getProvider] = useLazyGetProviderQuery();
	const [getProviderKeys] = useLazyGetProviderKeysQuery();
	const [createProvider] = useCreateProviderMutation();
	const [updateProvider] = useUpdateProviderMutation();
	const [deleteProvider] = useDeleteProviderMutation();
	const [createProviderKey] = useCreateProviderKeyMutation();
	const [updateProviderKey] = useUpdateProviderKeyMutation();
	const [deleteProviderKey] = useDeleteProviderKeyMutation();
	const [refreshModels] = useRefreshProviderModelsMutation();

	const [stage, setStage] = useState<Stage>("intro");
	const [plan, setPlan] = useState<MigrationPlan | undefined>();
	const [loadError, setLoadError] = useState<string | undefined>();
	const [steps, setSteps] = useState<MigrationStep[]>([]);
	const [result, setResult] = useState<MigrationResult | undefined>();

	// Start over whenever a different provider is opened.
	useEffect(() => {
		setStage("intro");
		setPlan(undefined);
		setLoadError(undefined);
		setSteps([]);
		setResult(undefined);
	}, [provider.name]);

	const api = useMemo<MigrationApi>(
		() => ({
			getProvider: (name) =>
				getProvider(name, false)
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			getProviderKeys: (name) =>
				getProviderKeys(name, false)
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			createProvider: (body) =>
				createProvider(body)
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			updateProvider: (name, body) =>
				updateProvider({ name: name as ModelProviderName, ...body })
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			deleteProvider: (name) =>
				deleteProvider(name)
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			createProviderKey: (p, key) =>
				createProviderKey({ provider: p, key })
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			updateProviderKey: (p, keyId, key) =>
				updateProviderKey({ provider: p, keyId, key })
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			deleteProviderKey: (p, keyId) =>
				deleteProviderKey({ provider: p, keyId })
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
			refreshModels: (p) =>
				refreshModels(p)
					.unwrap()
					.catch((err) => Promise.reject(wrapError(err))),
		}),
		[
			getProvider,
			getProviderKeys,
			createProvider,
			updateProvider,
			deleteProvider,
			createProviderKey,
			updateProviderKey,
			deleteProviderKey,
			refreshModels,
		],
	);

	const loadPlan = async () => {
		setStage("loading");
		setLoadError(undefined);
		try {
			const [sourceProvider, sourceKeys] = await Promise.all([api.getProvider(provider.name), api.getProviderKeys(provider.name)]);
			let existingTarget: ExistingTarget | undefined;
			if (sourceProvider.name.trim().toLowerCase() !== DATABRICKS_PROVIDER) {
				try {
					const target = await api.getProvider(DATABRICKS_PROVIDER);
					const targetKeys = await api.getProviderKeys(DATABRICKS_PROVIDER);
					existingTarget = { provider: target, keys: targetKeys };
				} catch (err) {
					if (getErrorStatus(err) !== 404) throw err;
				}
			}
			setPlan(buildDatabricksMigrationPlan(sourceProvider, sourceKeys, existingTarget));
			setStage("preview");
		} catch (err) {
			setLoadError(err instanceof Error ? err.message : getErrorMessage(err));
			setStage("intro");
		}
	};

	const runMigration = async () => {
		if (!plan) return;
		setStage("running");
		setSteps(buildMigrationSteps(plan));
		const outcome = await runDatabricksMigration(plan, api, setSteps);
		setResult(outcome);
		setStage("finished");
		if (outcome.ok) {
			dispatch(providersApi.util.invalidateTags(["Providers", { type: "ProviderKeys", id: DATABRICKS_PROVIDER }, "DBKeys", "VirtualKeys"]));
		}
	};

	const updateKeyValue = (tempId: string, value: SecretVar) => {
		setPlan((prev) =>
			prev
				? {
						...prev,
						keys: prev.keys.map((k) => (k.tempId === tempId ? { ...k, value } : k)),
					}
				: prev,
		);
	};

	const migrateDisabledReason = !hasAccess
		? t("providers.databricksMigration.needAccess")
		: providerFormIsDirty
			? t("providers.databricksMigration.saveFirst")
			: undefined;

	const canMigrate = !!plan && !migrateDisabledReason && !planNeedsInput(plan);

	return (
		<AlertDialog open={show}>
			<AlertDialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl" data-testid="databricks-migration-dialog">
				<AlertDialogHeader>
					<AlertDialogTitle className="flex items-center gap-2">
						<RenderProviderIcon provider={DATABRICKS_PROVIDER as ProviderIconType} size="sm" className="h-5 w-5 shrink-0" />
						{stage === "finished" && result
							? result.ok
								? t("providers.databricksMigration.complete")
								: t("providers.databricksMigration.failed")
							: t("providers.databricksMigration.title")}
					</AlertDialogTitle>
					<AlertDialogDescription asChild>
						<div className="space-y-2">
							{(stage === "intro" || stage === "loading") && (
								<>
									<p>
										<Trans
											i18nKey="providers.databricksMigration.introP1"
											ns="models"
											values={{ name: provider.name }}
											components={{ name: <span className="text-foreground font-medium" /> }}
										/>
									</p>
									<p>{t("providers.databricksMigration.introP2")}</p>
								</>
							)}
							{stage === "preview" && <p>{t("providers.databricksMigration.previewHint")}</p>}
							{stage === "running" && <p>{t("providers.databricksMigration.runningHint")}</p>}
							{stage === "finished" && result && <p>{result.message}</p>}
						</div>
					</AlertDialogDescription>
				</AlertDialogHeader>

				{loadError && (
					<Alert variant="destructive">
						<AlertTriangle className="h-4 w-4" />
						<AlertTitle>{t("providers.databricksMigration.loadErrorTitle")}</AlertTitle>
						<AlertDescription>{loadError}</AlertDescription>
					</Alert>
				)}

				{stage === "preview" && plan && (
					<MigrationPreview
						plan={plan}
						onWorkspaceUrlChange={(v) => setPlan({ ...plan, workspaceUrl: v })}
						onApiFormatChange={(v) => setPlan({ ...plan, apiFormat: v })}
						onKeyValueChange={updateKeyValue}
					/>
				)}

				{(stage === "running" || stage === "finished") && <MigrationProgress steps={steps} />}

				<AlertDialogFooter>
					{(stage === "intro" || stage === "loading") && (
						<>
							<AlertDialogCancel onClick={onDeferred} disabled={stage === "loading"} data-testid="databricks-migration-not-now">
								{t("providers.databricksMigration.notNow")}
							</AlertDialogCancel>
							<DisabledTooltip reason={migrateDisabledReason}>
								<Button onClick={loadPlan} disabled={!!migrateDisabledReason || stage === "loading"} data-testid="databricks-migration-start">
									{stage === "loading" && <Loader2 className="h-4 w-4 animate-spin" />}
									{t("providers.databricksMigration.letsMigrate")}
								</Button>
							</DisabledTooltip>
						</>
					)}
					{stage === "preview" && (
						<>
							<Button variant="ghost" onClick={() => setStage("intro")}>
								{tc("back")}
							</Button>
							<AlertDialogCancel onClick={onDeferred}>{t("providers.databricksMigration.notNow")}</AlertDialogCancel>
							<DisabledTooltip
								reason={migrateDisabledReason ?? (plan && planNeedsInput(plan) ? t("providers.databricksMigration.fillMissing") : undefined)}
							>
								<Button onClick={runMigration} disabled={!canMigrate} data-testid="databricks-migration-confirm">
									{t("providers.databricksMigration.migrate")}
								</Button>
							</DisabledTooltip>
						</>
					)}
					{stage === "finished" && result && (
						<>
							{!result.ok && (
								<AlertDialogCancel onClick={onDeferred} data-testid="databricks-migration-close">
									{tc("close")}
								</AlertDialogCancel>
							)}
							{result.ok && (
								<AlertDialogAction onClick={onMigrated} data-testid="databricks-migration-done">
									{t("providers.databricksMigration.goToDatabricks")}
								</AlertDialogAction>
							)}
						</>
					)}
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

function DisabledTooltip({ reason, children }: { reason?: string; children: ReactNode }) {
	if (!reason) return <>{children}</>;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span tabIndex={0}>{children}</span>
			</TooltipTrigger>
			<TooltipContent>{reason}</TooltipContent>
		</Tooltip>
	);
}

interface PreviewProps {
	plan: MigrationPlan;
	onWorkspaceUrlChange: (value: string) => void;
	onApiFormatChange: (value: DatabricksApiFormat) => void;
	onKeyValueChange: (tempId: string, value: SecretVar) => void;
}

function MigrationPreview({ plan, onWorkspaceUrlChange, onApiFormatChange, onKeyValueChange }: PreviewProps) {
	const { t } = useTranslation("models");
	const net = plan.providerSettings.network_config;
	const perf = plan.providerSettings.concurrency_and_buffer_size;
	const otherHeaders = Object.keys(net.extra_headers ?? {});

	return (
		<div className="space-y-4 text-sm" data-testid="databricks-migration-preview">
			<section className="space-y-2">
				<SectionTitle>{t("providers.databricksMigration.workspace")}</SectionTitle>
				<div className="grid gap-3 sm:grid-cols-[1fr_180px]">
					<div className="space-y-1">
						<Label htmlFor="databricks-migration-workspace-url">{t("providers.databricksMigration.workspaceUrl")}</Label>
						<Input
							id="databricks-migration-workspace-url"
							data-testid="databricks-migration-workspace-url"
							value={plan.workspaceUrl}
							placeholder="https://adb-1234567890123456.7.azuredatabricks.net"
							onChange={(e) => onWorkspaceUrlChange(e.target.value)}
							onBlur={(e) => onWorkspaceUrlChange(normalizeWorkspaceUrl(e.target.value) || e.target.value)}
						/>
					</div>
					<div className="space-y-1">
						<Label>{t("providers.databricksMigration.inferenceSurface")}</Label>
						<Select value={plan.apiFormat} onValueChange={(v) => onApiFormatChange(v as DatabricksApiFormat)}>
							<SelectTrigger data-testid="databricks-migration-api-format">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{(Object.keys(API_FORMAT_KEYS) as DatabricksApiFormat[]).map((format) => (
									<SelectItem key={format} value={format}>
										{t(API_FORMAT_KEYS[format])}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
				</div>
			</section>

			<section className="space-y-2">
				<SectionTitle>
					{plan.keys.length === 1 && plan.source.isKeyless
						? t("providers.databricksMigration.authentication")
						: t("providers.databricksMigration.keysCount", { count: plan.keys.length })}
					{plan.targetExists && (
						<Badge variant="secondary" className="ml-2">
							{t("providers.databricksMigration.addedToExisting")}
						</Badge>
					)}
				</SectionTitle>
				<div className="divide-y rounded-md border">
					{plan.keys.map((key) => (
						<div key={key.tempId} className="space-y-2 p-3" data-testid={`databricks-migration-key-${key.tempId}`}>
							<div className="flex flex-wrap items-center gap-2">
								<span className="font-medium">{key.name}</span>
								{key.fromHeader && (
									<Badge variant="outline" className="text-muted-foreground">
										{t("providers.databricksMigration.fromAuthHeader")}
									</Badge>
								)}
								{key.createName !== key.name && (
									<Badge variant="outline" className="text-muted-foreground">
										{t("providers.databricksMigration.createdAsRenamed", { name: key.createName })}
									</Badge>
								)}
							</div>
							<div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
								<span>
									{t("providers.databricksMigration.modelsLabel", {
										value: key.models.length === 0 || key.models.includes("*") ? t("providers.databricksMigration.modelsAll") : key.models.join(", "),
									})}
								</span>
								{key.blacklisted_models.length > 0 && (
									<span>{t("providers.databricksMigration.blacklisted", { value: key.blacklisted_models.join(", ") })}</span>
								)}
								<span>{t("providers.databricksMigration.weight", { value: key.weight })}</span>
								<span>{t("providers.databricksMigration.aliases", { value: Object.keys(key.aliases ?? {}).length })}</span>
								{!key.enabled && <span>{t("providers.databricksMigration.disabled")}</span>}
							</div>
							{key.needsValue || !isSecretVarSet(key.value) ? (
								<div className="space-y-1">
									<Label htmlFor={`databricks-migration-token-${key.tempId}`}>
										{t("providers.databricksMigration.patRequired")} <span className="text-destructive">*</span>
									</Label>
									<SecretVarInput
										id={`databricks-migration-token-${key.tempId}`}
										data-testid={`databricks-migration-token-${key.tempId}`}
										value={key.value ?? { value: "", ref: "" }}
										onChange={(v) => onKeyValueChange(key.tempId, v)}
										placeholder="dapi... or env.DATABRICKS_TOKEN"
									/>
									<p className="text-muted-foreground text-xs">
										{key.fromHeader ? t("providers.databricksMigration.tokenUnread") : t("providers.databricksMigration.tokenMasked")}
									</p>
								</div>
							) : (
								<div className="text-xs">
									<span className="text-muted-foreground">{t("providers.databricksMigration.patLabel")}</span>
									<code className="bg-muted rounded px-1 py-0.5 font-mono" data-testid={`databricks-migration-masked-${key.tempId}`}>
										{maskSecret(key.value)}
									</code>
								</div>
							)}
						</div>
					))}
				</div>
			</section>

			<section className="space-y-2">
				<SectionTitle>
					{t("providers.databricksMigration.providerSettings")}
					{plan.targetExists && (
						<Badge variant="secondary" className="ml-2">
							{t("providers.databricksMigration.existingSettingsKept")}
						</Badge>
					)}
				</SectionTitle>
				<dl className="text-muted-foreground grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
					<Stat label={t("providers.databricksMigration.timeout")} value={`${net.default_request_timeout_in_seconds}s`} />
					<Stat label={t("providers.databricksMigration.retries")} value={String(net.max_retries)} />
					<Stat
						label={t("providers.databricksMigration.backoff")}
						value={`${net.retry_backoff_initial}ms → ${net.retry_backoff_max}ms`}
					/>
					<Stat label={t("providers.databricksMigration.concurrency")} value={String(perf.concurrency)} />
					<Stat label={t("providers.databricksMigration.bufferSize")} value={String(perf.buffer_size)} />
					<Stat label={t("providers.databricksMigration.proxy")} value={plan.providerSettings.proxy_config?.type ?? t("providers.databricksMigration.none")} />
					<Stat
						label={t("providers.databricksMigration.extraHeaders")}
						value={otherHeaders.length > 0 ? otherHeaders.join(", ") : t("providers.databricksMigration.none")}
					/>
					<Stat
						label={t("providers.databricksMigration.privateNetwork")}
						value={net.allow_private_network ? t("providers.databricksMigration.allowed") : t("providers.databricksMigration.blocked")}
					/>
					<Stat
						label={t("providers.databricksMigration.rawReqRes")}
						value={
							plan.providerSettings.send_back_raw_request || plan.providerSettings.send_back_raw_response
								? t("providers.databricksMigration.on")
								: t("providers.databricksMigration.off")
						}
					/>
				</dl>
			</section>

			{plan.warnings.length > 0 && (
				<Alert variant={plan.nameClash ? "destructive" : "default"} data-testid="databricks-migration-warnings">
					<AlertTriangle className="h-4 w-4" />
					<AlertTitle>{t("providers.databricksMigration.beforeContinue")}</AlertTitle>
					<AlertDescription>
						<ul className="list-disc space-y-1 pl-4">
							{plan.warnings.map((warning) => (
								<li key={warning}>{warning}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			)}
		</div>
	);
}

function MigrationProgress({ steps }: { steps: MigrationStep[] }) {
	return (
		<ol className="space-y-2 text-sm" data-testid="databricks-migration-progress">
			{steps.map((step) => (
				<li key={step.id} className="flex items-start gap-2" data-testid={`databricks-migration-step-${step.id}`} data-status={step.status}>
					<StepIcon status={step.status} />
					<div className="min-w-0 flex-1">
						<div className={step.status === "pending" ? "text-muted-foreground" : ""}>{step.label}</div>
						{step.detail && (
							<div className={`mt-0.5 text-xs whitespace-pre-line ${step.status === "failed" ? "text-destructive" : "text-muted-foreground"}`}>
								{step.detail}
							</div>
						)}
					</div>
				</li>
			))}
		</ol>
	);
}

function StepIcon({ status }: { status: MigrationStep["status"] }) {
	switch (status) {
		case "running":
			return <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin" />;
		case "done":
			return <Check className="mt-0.5 h-4 w-4 shrink-0 text-green-600" />;
		case "failed":
			return <X className="text-destructive mt-0.5 h-4 w-4 shrink-0" />;
		case "skipped":
			return <CircleDashed className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />;
		default:
			return <CircleDashed className="text-muted-foreground/50 mt-0.5 h-4 w-4 shrink-0" />;
	}
}

function SectionTitle({ children }: { children: ReactNode }) {
	return <h4 className="flex items-center text-xs font-semibold tracking-wide uppercase">{children}</h4>;
}

function Stat({ label, value }: { label: string; value: string }) {
	return (
		<div className="min-w-0">
			<dt className="inline">{label}: </dt>
			<dd className="text-foreground inline break-all">{value}</dd>
		</div>
	);
}
