import Provider from "@/components/provider";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ModelProvider } from "@/lib/types/config";
import { toast } from "sonner";
import ProviderKeyForm from "../views/providerKeyForm";

interface Props {
	show: boolean;
	onCancel: () => void;
	provider: ModelProvider;
	keyIndex: number;
}

export default function AddNewKeyDialog({ show, onCancel, provider, keyIndex }: Props) {
	const isEditing = keyIndex < provider.keys.length;
	const dialogTitle = isEditing ? "Edit key" : "Add new key";
	const successMessage = isEditing ? "Key updated successfully" : "Key added successfully";

	return (
		<Dialog open={show} onOpenChange={onCancel}>
			<DialogContent className="custom-scrollbar max-h-[60vh] max-w-[400px] min-w-[35vw] overflow-y-scroll">
				<DialogHeader>
					<DialogTitle>
						<div className="font-lg flex items-center gap-2">
							<div className={"flex items-center"}>
								<Provider provider={provider.name} size={24} />:
							</div>
							{dialogTitle}
						</div>
					</DialogTitle>
				</DialogHeader>
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
			</DialogContent>
		</Dialog>
	);
}
