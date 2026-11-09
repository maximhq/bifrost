import { Button } from "@/components/ui/button";
import { getErrorMessage } from "@/lib/store";
import { useSetModelRouterTargetsMutation } from "@/lib/store/apis/governanceApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { DBKey } from "@/lib/types/governance";
import { ModelProvider } from "@/lib/types/config";
import { LoaderCircle, Save } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
	TierTargetErrors,
	TierTargetFormData,
	TierTargetsEditor,
	toTierTargetForms,
	toTierTargetPayload,
	validateTierTargets,
} from "./tierTargetsEditor";

const sameTargets = (a: TierTargetFormData[], b: TierTargetFormData[]) =>
	JSON.stringify(toTierTargetPayload(a)) === JSON.stringify(toTierTargetPayload(b));

interface ModelsTabProps {
	router: ModelRouter;
	canUpdate: boolean;
	providers: ModelProvider[];
	keys: DBKey[];
}

// ModelsTab picks the model each tier of the router sends requests to. They
// are saved on their own, apart from the classifier's configuration and the
// router's routing.
export function ModelsTab({ router, canUpdate, providers, keys }: ModelsTabProps) {
	const { t } = useTranslation("models");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters.models" });
	const { t: tc } = useTranslation("common");
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const [setTargets, { isLoading: isSaving }] = useSetModelRouterTargetsMutation();
	const savedForms = useMemo(() => toTierTargetForms(router.targets), [router.targets]);
	const [targets, setTargetForms] = useState<TierTargetFormData[]>(savedForms);
	const [errors, setErrors] = useState<TierTargetErrors>({});

	const isDirty = !sameTargets(targets, savedForms);
	// A save elsewhere (another tab, another operator) refreshes the router;
	// the draft follows it unless it holds unsaved edits.
	const lastSaved = useRef(savedForms);
	useEffect(() => {
		const previous = lastSaved.current;
		setTargetForms((current) => (sameTargets(current, previous) ? savedForms : current));
		lastSaved.current = savedForms;
	}, [savedForms]);
	// Errors already shown follow the edits that fix them.
	useEffect(() => {
		setErrors((current) => (Object.keys(current).length > 0 ? validateTierTargets(targets, t) : current));
	}, [targets, t]);

	const configuredProviders = useMemo(() => providers.map((provider) => provider.name), [providers]);

	const save = () => {
		const checked = validateTierTargets(targets, t);
		setErrors(checked);
		if (Object.keys(checked).length > 0) return;
		setTargets({ id: router.id, targets: toTierTargetPayload(targets) })
			.unwrap()
			.then(() => toast.success(tmr("saved"), { position: "top-right" }))
			.catch((err) => toast.error(tmr("saveFailed", { error: getErrorMessage(err) })));
	};

	return (
		<div className="flex min-h-0 grow flex-col" data-testid="model-router-models">
			<div className="custom-scrollbar min-h-0 flex-1 space-y-4 overflow-y-auto pb-6">
				<p className="text-muted-foreground text-sm">{tmr("description")}</p>
				<TierTargetsEditor
					targets={targets}
					onChange={setTargetForms}
					errors={errors}
					configuredProviders={configuredProviders}
					keys={keys}
					disabled={!canUpdate}
				/>
			</div>
			<div className="flex flex-wrap items-center justify-end gap-2.5 border-t pt-4">
				<Button
					type="button"
					variant="outline"
					size="sm"
					onClick={() => {
						setTargetForms(savedForms);
						setErrors({});
					}}
					disabled={!isDirty || isSaving}
					data-testid="model-router-models-discard-button"
				>
					{tcpx("discardChanges")}
				</Button>
				<Button
					type="button"
					size="sm"
					onClick={save}
					disabled={!canUpdate || !isDirty || isSaving}
					data-testid="model-router-models-save-button"
				>
					{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
					{tc("save")}
				</Button>
			</div>
		</div>
	);
}