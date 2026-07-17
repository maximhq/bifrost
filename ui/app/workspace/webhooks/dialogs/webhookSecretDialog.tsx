import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { ArrowUpRight, Check, Copy } from "lucide-react";

const WEBHOOKS_VERIFICATION_DOCS_URL = "https://docs.getbifrost.ai/features/webhooks?utm_source=bfd#verifying-deliveries";

export interface WebhookSecretReveal {
	endpointName: string;
	secret: string;
}

interface WebhookSecretDialogProps {
	reveal: WebhookSecretReveal | null;
	onClose: () => void;
}

// The signing secret is returned exactly once by create and rotate-secret;
// the API never exposes it again, so this dialog is the only chance to copy it.
export function WebhookSecretDialog({ reveal, onClose }: WebhookSecretDialogProps) {
	const { copy, copied } = useCopyToClipboard();

	return (
		<Dialog open={!!reveal} onOpenChange={(open) => !open && onClose()}>
			<DialogContent data-testid="webhook-secret-dialog">
				<DialogHeader>
					<DialogTitle>Signing secret for {reveal?.endpointName}</DialogTitle>
					<DialogDescription>
						Use this secret to verify the <code>webhook-signature</code> header on deliveries. It is shown only once - copy it now and store
						it securely. If you lose it, rotate the endpoint to get a new one.
					</DialogDescription>
				</DialogHeader>
				<div className="flex items-center gap-2 rounded-sm border p-3">
					<code className="min-w-0 flex-1 font-mono text-sm break-all" data-testid="webhook-secret-value">
						{reveal?.secret}
					</code>
					<Button
						variant="ghost"
						size="sm"
						aria-label="Copy signing secret"
						onClick={() => reveal && copy(reveal.secret)}
						data-testid="webhook-secret-copy-btn"
					>
						{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
					</Button>
					<span className="sr-only" aria-live="polite">
						{copied ? "Signing secret copied" : ""}
					</span>
				</div>
				<DialogFooter>
					<Button
						variant="outline"
						onClick={() => {
							window.open(WEBHOOKS_VERIFICATION_DOCS_URL, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button onClick={onClose} data-testid="webhook-secret-done-btn">
						I&apos;ve stored the secret
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}