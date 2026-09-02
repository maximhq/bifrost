import Provider from "@/components/provider";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ModelProvider } from "@/lib/types/config";
import { toast } from "sonner";
import ProviderKeyForm from "../views/providerKeyForm";
import { useTranslation } from "react-i18next";

interface Props {
	show: boolean;
	onCancel: () => void;
	provider: ModelProvider;
	keyId: string | null;
	providerName?: string;
}

export default function AddNewKeySheet({ show, onCancel, provider, keyId, providerName }: Props) {
	const { t } = useTranslation("models");
	const isEditing = keyId !== null;
	const resolvedProviderName = (providerName ?? provider.name).toLowerCase();
	const isVLLM = resolvedProviderName === "vllm";
	const isOllamaOrSGL = resolvedProviderName === "ollama" || resolvedProviderName === "sgl";
	const entityLabelKey = isVLLM ? "entityModel" : isOllamaOrSGL ? "entityServer" : "entityKey";
	const entityLabelCapKey = isVLLM ? "entityModelCap" : isOllamaOrSGL ? "entityServerCap" : "entityKeyCap";
	const entityLabel = t(`providers.${entityLabelKey}`);
	const EntityLabel = t(`providers.${entityLabelCapKey}`);
	const dialogTitle = isEditing ? t("providers.editKeyEntity", { entity: entityLabel }) : t("providers.addKeyEntity", { entity: entityLabel });
	const successMessage = isEditing ? t("providers.entityUpdated", { entity: EntityLabel }) : t("providers.entityAdded", { entity: EntityLabel });

	return (
		<Sheet
			open={show}
			onOpenChange={(open) => {
				if (!open) onCancel();
			}}
		>
			<SheetContent className="p-0 pt-4" data-testid="key-form" onInteractOutside={(e) => e.preventDefault()}>
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8 ">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<div className={"flex items-center"}>
								<Provider provider={provider.name} size={24} />:
							</div>
							{dialogTitle}
						</div>
					</SheetTitle>
				</SheetHeader>
				<ProviderKeyForm
					provider={provider}
					keyId={keyId}
					onCancel={onCancel}
					onSave={() => {
						toast.success(successMessage);
						onCancel();
					}}
				/>
			</SheetContent>
		</Sheet>
	);
}