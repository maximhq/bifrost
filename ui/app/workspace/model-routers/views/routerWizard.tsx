import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { WizardLayout, type WizardStep } from "@/components/wizardLayout";
import { useCreateModelRouterMutation, useGetModelRouterDefaultsQuery } from "@/lib/store/apis/governanceApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ArrowLeft, ArrowRight, LoaderCircle } from "lucide-react";
import { useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
	RouterConfigurationActions,
	RouterConfigurationEditor,
	RouterConfigurationRestoreButton,
	RouterConfigurationSheets,
} from "./routerConfigurationEditor";
import { modelRouterIdentitySchema, type ModelRouterIdentityFormSchema } from "@/lib/types/schemas";
import { RouterIdentityFields } from "./routerIdentityFields";
import { ClassifierChoice } from "@/app/workspace/complexity-router/views/sections";
import { useRouterConfigForm } from "./useRouterConfigForm";

const STEP_KEYS = ["general", "classifier", "configuration"] as const;

interface RouterWizardProps {
	onCancel: () => void;
	// Called with the new router's id once it is created.
	onCreated: (id: string) => void;
}

// RouterWizard creates a model router in three steps: its name, its
// classifier, and what that classifier works from. Nothing is saved until the
// last step, so leaving part-way leaves nothing behind. The router's models
// and routing are set on its page once it exists.
export default function RouterWizard({ onCancel, onCreated }: RouterWizardProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: tc } = useTranslation("common");
	const canCreate = useRbac(RbacResource.Routers, RbacOperation.Create);
	const { data: defaults, isLoading, error, refetch } = useGetModelRouterDefaultsQuery();
	const [createRouter] = useCreateModelRouterMutation();
	const [stepIndex, setStepIndex] = useState(0);

	const identity = useForm<ModelRouterIdentityFormSchema>({
		resolver: zodResolver(modelRouterIdentitySchema),
		defaultValues: { name: "", description: "" },
	});
	const createDefaults = useMemo(
		() => (defaults ? { decision: defaults.decision_defaults, llmPrompt: defaults.llm_default_prompt } : undefined),
		[defaults],
	);
	const config = useRouterConfigForm({
		saved: defaults?.config,
		canUpdate: canCreate,
		createDefaults,
		save: async (payload) => {
			const { name, description } = identity.getValues();
			const created = await createRouter({ ...payload, name, description } as ModelRouter).unwrap();
			toast.success(t("created"), { position: "top-right" });
			onCreated(created.id);
			return created;
		},
	});

	const steps: WizardStep[] = STEP_KEYS.map((key) => ({
		key,
		title: t(`wizard.${key}`),
		description: t(`wizard.${key}Description`),
	}));
	const stepKey = STEP_KEYS[stepIndex];

	const next = async () => {
		if (stepKey === "general" && !(await identity.trigger())) return;
		const nextIndex = Math.min(STEP_KEYS.length - 1, stepIndex + 1);
		setStepIndex(nextIndex);
		// Semantic cannot classify without an embedding model, so go straight
		// to the one place that sets it.
		if (STEP_KEYS[nextIndex] === "configuration" && config.live.classifier === "semantic" && !config.derived.isClassifierConfigured) {
			config.sheets.setOpen("embedding");
		}
	};

	const pageTitle = <PageTitle breadcrumbs={[{ label: t("title"), to: "/workspace/model-routers" }, { label: t("newRouter") }]} beta />;

	if (isLoading) {
		return (
			<>
				{pageTitle}
				<FullPageLoader />
			</>
		);
	}
	if (!defaults) {
		return (
			<>
				{pageTitle}
				<LoadErrorView
					entity="model router defaults"
					error={error}
					onRetry={() => void refetch()}
					testId="model-router-defaults-load-error"
				/>
			</>
		);
	}

	return (
		<div className="no-padding-parent flex h-full min-h-0 w-full flex-col p-4" data-testid="model-router-wizard">
			{pageTitle}
			<WizardLayout
				steps={steps}
				currentIndex={stepIndex}
				onSelect={setStepIndex}
				testIdPrefix="model-router-wizard"
				actions={stepKey === "configuration" ? <RouterConfigurationActions config={config} /> : undefined}
				footer={
					<>
						<Button
							type="button"
							variant="ghost"
							onClick={stepIndex === 0 ? onCancel : () => setStepIndex(stepIndex - 1)}
							disabled={config.isSaving}
							data-testid="model-router-wizard-back"
						>
							<ArrowLeft className="h-4 w-4" />
							{stepIndex === 0 ? t("title") : tc("back")}
						</Button>
						<div className="flex items-center gap-2">
							{stepKey === "configuration" && <RouterConfigurationRestoreButton config={config} />}
							{stepKey === "configuration" ? (
								<Button
									type="button"
									onClick={() => void config.submit()}
									disabled={!config.canSave || config.isSaving}
									data-testid="model-router-wizard-create"
								>
									{config.isSaving && <LoaderCircle className="h-4 w-4 animate-spin" />}
									{t("createRouter")}
								</Button>
							) : (
								<Button type="button" onClick={() => void next()} data-testid="model-router-wizard-next">
									{tc("next")}
									<ArrowRight className="h-4 w-4" />
								</Button>
							)}
						</div>
					</>
				}
			>
				{stepKey === "general" && (
					<Form {...identity}>
						<form
							className="max-w-3xl space-y-4"
							onSubmit={(event) => {
								event.preventDefault();
								void next();
							}}
							noValidate
						>
							<RouterIdentityFields control={identity.control} />
						</form>
					</Form>
				)}
				{stepKey === "classifier" && <ClassifierChoice value={config.live.classifier} onChange={config.selectClassifier} includeLlm />}
				{stepKey === "configuration" && <RouterConfigurationEditor config={config} />}
			</WizardLayout>
			<RouterConfigurationSheets config={config} />
		</div>
	);
}