import { Button } from "@/components/ui/button";
import { Waypoints } from "lucide-react";

export function EndpointsEmptyState({ onCreate, canCreate }: { onCreate: () => void; canCreate: boolean }) {
	return (
		<div
			className="flex flex-col items-center justify-center gap-4 rounded-md border border-dashed px-6 py-16 text-center"
			data-testid="relays-empty"
		>
			<Waypoints className="text-muted-foreground h-10 w-10" />
			<div className="space-y-1">
				<h2 className="text-lg font-semibold">Relays</h2>
				<p className="text-muted-foreground max-w-md text-sm">
					Relay non-LLM APIs (search, OCR, speech, AWS services) through Bifrost with one credential, per-route pricing, virtual key access
					and budgets.
				</p>
			</div>
			<Button onClick={onCreate} disabled={!canCreate} data-testid="relays-create-first">
				Add relay
			</Button>
		</div>
	);
}