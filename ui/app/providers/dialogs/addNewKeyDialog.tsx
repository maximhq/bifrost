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
	return (
		<Dialog open={show} onOpenChange={onCancel}>
			<DialogContent className="max-h-[60vh] max-w-[400px] min-w-[35vw] overflow-y-auto">
				<DialogHeader>
					<DialogTitle>
						<div className="font-lg flex items-center gap-2">
							<div className={"flex items-center"}>
								<Provider provider={provider.name} size={24} />:
							</div>
							Add new key
						</div>
					</DialogTitle>
				</DialogHeader>
				<div>
					<ProviderKeyForm
						provider={provider}
						keyIndex={keyIndex}
						onCancel={onCancel}
						onSave={() => {
							toast.success("Key added successfully");
							onCancel();
						}}
					/>
				</div>
			</DialogContent>
		</Dialog>
	);
}
