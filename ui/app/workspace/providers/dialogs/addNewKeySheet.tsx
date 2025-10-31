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
}

export default function AddNewKeySheet({ show, onCancel, provider, keyIndex }: Props) {
	const isEditing = keyIndex < provider.keys.length;
	const dialogTitle = isEditing ? "Edit key" : "Add new key";
	const successMessage = isEditing ? "Key updated successfully" : "Key added successfully";

	return (
		<Sheet open={show} onOpenChange={onCancel}>
			<SheetContent className="custom-scrollbar bg-white dark:bg-card min-w-[600px] py-4">
				<SheetHeader>
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<div className={"flex items-center"}>
								<Provider provider={provider.name} size={24} />:
							</div>
							{dialogTitle}
						</div>
					</SheetTitle>
				</SheetHeader>
				<div className="px-4">
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
