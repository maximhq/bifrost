import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useT } from "@/lib/i18n";
import { getErrorMessage, useForcePricingSyncMutation, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import {
	DEFAULT_LIVE_MODELS_SYNC_INTERVAL,
	DefaultCoreConfig,
	LIVE_MODELS_SYNC_DISABLED,
	MIN_LIVE_MODELS_SYNC_INTERVAL,
} from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface ModelSettingsFormData {
	pricing_datasheet_url: string;
	pricing_sync_interval_hours: number;
	model_parameters_url: string;
	routing_chain_max_depth: number;
	/**
	 * Minutes rather than hours, unlike the pricing interval above: the useful
	 * range here starts well below an hour for anyone fronting a fast-moving
	 * upstream. 0 means the background refresh is off.
	 */
	live_models_sync_interval_minutes: number;
}

const SECONDS_PER_MINUTE = 60;

/** Server seconds to form minutes, falling back to the default when unset. */
function toSyncMinutes(intervalSeconds: number | undefined): number {
	if (intervalSeconds === LIVE_MODELS_SYNC_DISABLED) return 0;
	const effective = intervalSeconds ?? DEFAULT_LIVE_MODELS_SYNC_INTERVAL;
	return Math.round(effective / SECONDS_PER_MINUTE);
}

export default function ModelSettingsView() {
	const t = useT();
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const frameworkConfig = bifrostConfig?.framework_config;
	const clientConfig = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [forcePricingSync, { isLoading: isForceSyncing }] = useForcePricingSyncMutation();

	const {
		register,
		handleSubmit,
		formState: { errors, isDirty },
		reset,
		watch,
	} = useForm<ModelSettingsFormData>({
		defaultValues: {
			pricing_datasheet_url: "",
			pricing_sync_interval_hours: 24,
			model_parameters_url: "",
			routing_chain_max_depth: DefaultCoreConfig.routing_chain_max_depth,
			live_models_sync_interval_minutes: DEFAULT_LIVE_MODELS_SYNC_INTERVAL / SECONDS_PER_MINUTE,
		},
	});

	const formValues = watch();

	useEffect(() => {
		if (!bifrostConfig || isDirty) return;
		reset({
			pricing_datasheet_url: frameworkConfig?.pricing_url || "",
			pricing_sync_interval_hours: Math.round((frameworkConfig?.pricing_sync_interval ?? 0) / 3600) || 24,
			model_parameters_url: frameworkConfig?.model_parameters_url || "",
			routing_chain_max_depth: clientConfig?.routing_chain_max_depth ?? DefaultCoreConfig.routing_chain_max_depth,
			live_models_sync_interval_minutes: toSyncMinutes(frameworkConfig?.live_models_sync_interval),
		});
	}, [
		frameworkConfig?.pricing_url,
		frameworkConfig?.pricing_sync_interval,
		frameworkConfig?.model_parameters_url,
		frameworkConfig?.live_models_sync_interval,
		clientConfig?.routing_chain_max_depth,
		isDirty,
		reset,
	]);

	const hasChanges = useMemo(() => {
		if (!bifrostConfig || !isDirty) return false;
		const serverUrl = frameworkConfig?.pricing_url || "";
		const serverInterval = Math.round((frameworkConfig?.pricing_sync_interval ?? 0) / 3600);
		const serverModelParamsUrl = frameworkConfig?.model_parameters_url || "";
		const serverDepth = clientConfig?.routing_chain_max_depth ?? DefaultCoreConfig.routing_chain_max_depth;
		const serverLiveModelsMinutes = toSyncMinutes(frameworkConfig?.live_models_sync_interval);
		return (
			formValues.pricing_datasheet_url !== serverUrl ||
			formValues.pricing_sync_interval_hours !== serverInterval ||
			formValues.model_parameters_url !== serverModelParamsUrl ||
			formValues.routing_chain_max_depth !== serverDepth ||
			formValues.live_models_sync_interval_minutes !== serverLiveModelsMinutes
		);
	}, [bifrostConfig, frameworkConfig, clientConfig, formValues, isDirty]);

	const onSubmit = async (data: ModelSettingsFormData) => {
		try {
			await updateCoreConfig({
				...bifrostConfig!,
				framework_config: {
					...frameworkConfig,
					id: bifrostConfig?.framework_config.id || 0,
					pricing_url: data.pricing_datasheet_url,
					pricing_sync_interval: data.pricing_sync_interval_hours * 3600,
					model_parameters_url: data.model_parameters_url,
					live_models_sync_interval: data.live_models_sync_interval_minutes * SECONDS_PER_MINUTE,
				},
				client_config: {
					...clientConfig!,
					routing_chain_max_depth: data.routing_chain_max_depth,
				},
			}).unwrap();
			toast.success(t("configForms.modelSettings.updated"));
			reset(data);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleForceSync = async () => {
		try {
			await forcePricingSync().unwrap();
			toast.success(t("configForms.modelSettings.pricingSynced"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4" data-testid="model-settings-view">
			<form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
				<PageTitle title="Model Settings">{t("configForms.modelSettings.pageDescription")}</PageTitle>

				<div className="space-y-4">
					{/* Pricing Datasheet URL */}
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="pricing-datasheet-url">{t("configForms.modelSettings.pricingDatasheetUrl")}</Label>
							<p className="text-muted-foreground text-sm">{t("configForms.modelSettings.pricingDatasheetUrlDescription")}</p>
						</div>
						<Input
							id="pricing-datasheet-url"
							type="text"
							placeholder={t("configForms.modelSettings.pricingDatasheetUrlPlaceholder")}
							data-testid="pricing-datasheet-url-input"
							{...register("pricing_datasheet_url", {
								validate: {
									checkIfValidUrl: (value) => {
										if (!value) return true;
										return (
											value.startsWith("http://") ||
											value.startsWith("https://") ||
											value.startsWith("file://") ||
											t("configForms.modelSettings.invalidUrlScheme")
										);
									},
								},
							})}
							className={errors.pricing_datasheet_url ? "border-destructive" : ""}
						/>
						{errors.pricing_datasheet_url && <p className="text-destructive text-sm">{errors.pricing_datasheet_url.message}</p>}
					</div>

					{/* Model Parameters URL */}
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="model-parameters-url">{t("configForms.modelSettings.modelParametersUrl")}</Label>
							<p className="text-muted-foreground text-sm">{t("configForms.modelSettings.modelParametersUrlDescription")}</p>
						</div>
						<Input
							id="model-parameters-url"
							type="text"
							placeholder={t("configForms.modelSettings.modelParametersUrlPlaceholder")}
							data-testid="model-parameters-url-input"
							{...register("model_parameters_url", {
								validate: {
									checkIfValidUrl: (value) => {
										if (!value) return true;
										return (
											value.startsWith("http://") ||
											value.startsWith("https://") ||
											value.startsWith("file://") ||
											t("configForms.modelSettings.invalidUrlScheme")
										);
									},
								},
							})}
							className={errors.model_parameters_url ? "border-destructive" : ""}
						/>
						{errors.model_parameters_url && <p className="text-destructive text-sm">{errors.model_parameters_url.message}</p>}
					</div>

					{/* Pricing Sync Interval */}
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="pricing-sync-interval">{t("configForms.modelSettings.pricingSyncInterval")}</Label>
							<p className="text-muted-foreground text-sm">{t("configForms.modelSettings.pricingSyncIntervalDescription")}</p>
						</div>
						<Input
							id="pricing-sync-interval"
							type="number"
							data-testid="pricing-sync-interval-input"
							className={errors.pricing_sync_interval_hours ? "border-destructive" : ""}
							{...register("pricing_sync_interval_hours", {
								required: t("configForms.modelSettings.pricingSyncRequired"),
								min: { value: 1, message: t("configForms.modelSettings.pricingSyncMin") },
								max: { value: 8760, message: t("configForms.modelSettings.pricingSyncMax") },
								valueAsNumber: true,
							})}
						/>
						{errors.pricing_sync_interval_hours && <p className="text-destructive text-sm">{errors.pricing_sync_interval_hours.message}</p>}
					</div>

					{/* Model Discovery Interval */}
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="live-models-sync-interval">{t("configForms.modelSettings.discoveryInterval")}</Label>
							<p className="text-muted-foreground text-sm">{t("configForms.modelSettings.discoveryIntervalDescription")}</p>
						</div>
						<Input
							id="live-models-sync-interval"
							type="number"
							data-testid="live-models-sync-interval-input"
							className={errors.live_models_sync_interval_minutes ? "border-destructive" : ""}
							{...register("live_models_sync_interval_minutes", {
								required: t("configForms.modelSettings.discoveryRequired"),
								validate: (value) => {
									if (value === 0) return true;
									if (value < MIN_LIVE_MODELS_SYNC_INTERVAL / SECONDS_PER_MINUTE) {
										return t("configForms.modelSettings.discoveryMin", {
											minutes: MIN_LIVE_MODELS_SYNC_INTERVAL / SECONDS_PER_MINUTE,
										});
									}
									if (value > 1440) return t("configForms.modelSettings.discoveryMax");
									return true;
								},
								valueAsNumber: true,
							})}
						/>
						{errors.live_models_sync_interval_minutes && (
							<p className="text-destructive text-sm">{errors.live_models_sync_interval_minutes.message}</p>
						)}
						{formValues.live_models_sync_interval_minutes === 0 && !errors.live_models_sync_interval_minutes && (
							<p className="text-muted-foreground text-sm">{t("configForms.modelSettings.discoveryOff")}</p>
						)}
					</div>

					{/* Routing Chain Max Depth */}
					<div className="flex items-center justify-between rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="routing-chain-max-depth">{t("configForms.modelSettings.routingChainMaxDepth")}</Label>
							<p className="text-muted-foreground text-sm">{t("configForms.modelSettings.routingChainMaxDepthDescription")}</p>
						</div>
						<Input
							id="routing-chain-max-depth"
							type="number"
							className={`w-24 ${errors.routing_chain_max_depth ? "border-destructive" : ""}`}
							data-testid="routing-chain-max-depth-input"
							{...register("routing_chain_max_depth", {
								required: t("configForms.modelSettings.routingDepthRequired"),
								min: { value: 1, message: t("configForms.modelSettings.routingDepthMin") },
								max: { value: 100, message: t("configForms.modelSettings.routingDepthMax") },
								valueAsNumber: true,
							})}
						/>
					</div>
					{errors.routing_chain_max_depth && <p className="text-destructive text-sm">{errors.routing_chain_max_depth.message}</p>}
				</div>

				<div className="flex justify-end gap-2 pt-2">
					<Button
						variant="outline"
						type="button"
						onClick={handleForceSync}
						disabled={isForceSyncing || isLoading || hasChanges || !hasSettingsUpdateAccess}
						data-testid="pricing-force-sync-btn"
					>
						{isForceSyncing ? t("configForms.modelSettings.syncing") : t("configForms.modelSettings.forceSync")}
					</Button>
					<Button type="submit" disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="model-settings-save-btn">
						{isLoading ? t("common.saving") : t("common.saveChanges")}
					</Button>
				</div>
			</form>
		</div>
	);
}