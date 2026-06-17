// Confirmation dialog for revoking an OAuth grant. Open/confirm are driven by
// the page; the copy explains that the refresh token stops rotating immediately
// while the current short-lived access token keeps working until it expires.

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";

interface RevokeGrantDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onConfirm: () => void;
}

export default function RevokeGrantDialog({ open, onOpenChange, onConfirm }: RevokeGrantDialogProps) {
	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Revoke this OAuth grant?</AlertDialogTitle>
					<AlertDialogDescription>
						The refresh token for this grant stops rotating right away, so the
						MCP client can no longer renew its access. Its current access token
						is a short-lived JWT that keeps working on the{" "}
						<code className="rounded bg-muted px-1 py-0.5 text-xs">/mcp</code>{" "}
						endpoint until it expires (up to 10 minutes), after which the client
						is fully cut off and must reconnect via the OAuth consent flow.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel data-testid="oauth-grants-revoke-cancel-btn">
						Cancel
					</AlertDialogCancel>
					<AlertDialogAction
						data-testid="oauth-grants-revoke-confirm-btn"
						onClick={onConfirm}
					>
						Revoke
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
