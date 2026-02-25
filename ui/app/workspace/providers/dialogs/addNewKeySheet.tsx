import Provider from "@/components/provider";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ModelProvider } from "@/lib/types/config";
import { toast } from "sonner";
import ProviderKeyForm from "../views/providerKeyForm";

interface Props {
	show: boolean;
	onCancel: () => void;
	provider: ModelProvider;
	keyIndex: number;
	providerName?: string;
}

export default function AddNewKeySheet({ show, onCancel, provider, keyIndex, providerName }: Props) {
	const isEditing = keyIndex < provider.keys.length;
	const resolvedProviderName = (providerName ?? provider.name).toLowerCase();
	const isVLLM = resolvedProviderName === "vllm";
	const entityLabel = isVLLM ? "model" : "key";
	const EntityLabel = entityLabel.charAt(0).toUpperCase() + entityLabel.slice(1);
	const dialogTitle = isEditing ? `Edit ${entityLabel}` : `Add new ${entityLabel}`;
	const successMessage = isEditing ? `${EntityLabel} updated successfully` : `${EntityLabel} added successfully`;

	return (
		<Sheet
			open={show}
			onOpenChange={(open) => {
				if (!open) onCancel();
			}}
		>
			<SheetContent className="custom-scrollbar dark:bg-card bg-white p-8" data-testid="key-form">
				<SheetHeader className="flex flex-col items-start">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<div className={"flex items-center"}>
								<Provider provider={provider.name} size={24} />:
							</div>
							{dialogTitle}
						</div>
					</SheetTitle>
				</SheetHeader>
				<div>
					<ProviderKeyForm
						provider={provider}
						keyIndex={keyIndex}
						onCancel={onCancel}
						onSave={() => {
							toast.success(successMessage);
							onCancel();
						}}
					/>
				</div>
			</SheetContent>
		</Sheet>
	);
}
