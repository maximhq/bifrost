import Provider from "@/components/provider";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useAppSelector } from "@/lib/store";

export default function AddNewKeyDialog({ show, onCancel }: { show: boolean; onCancel: () => void }) {
	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	return (
		<Dialog open={show} onOpenChange={onCancel}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>
						<div className="font-lg flex items-center gap-2">
							<div className="flex items-center gap-2">
								<Provider provider={selectedProvider?.name ?? "custom"} size={20} />:
							</div>
							Add new key
						</div>
					</DialogTitle>
				</DialogHeader>
			</DialogContent>
		</Dialog>
	);
}
