import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { TriangleAlert, X } from "lucide-react";
import { useTranslation } from "react-i18next";

// ToolsPendingNotice tells a caller who just signed in to an MCP client that its
// tools, prompts and resources could not be discovered with the new credential.
// The credential is stored, so connecting to the client again from their MCP
// client retries. With onDismiss it carries a close button.
export function ToolsPendingNotice({
	clientName,
	error,
	className,
	onDismiss,
}: {
	clientName: string;
	error?: string;
	className?: string;
	onDismiss?: () => void;
}) {
	const { t } = useTranslation("mcp");
	return (
		<Alert
			variant="warning"
			className={cn("text-left", onDismiss && "has-[>svg]:grid-cols-[calc(var(--spacing)*4)_1fr_auto]", className)}
			data-testid="mcp-sessions-tools-pending-alert"
		>
			<TriangleAlert />
			<AlertTitle>{t("sessions.auth.toolsPendingTitle")}</AlertTitle>
			<AlertDescription>
				<p>{t("sessions.auth.toolsPendingBody", { name: clientName })}</p>
				{error && (
					<p className="font-mono text-xs break-all" data-testid="mcp-sessions-tools-pending-error">
						{error}
					</p>
				)}
			</AlertDescription>
			{onDismiss && (
				<Button
					type="button"
					variant="ghost"
					size="icon"
					className="col-start-3 row-span-2 row-start-1 -my-1.5 size-7"
					onClick={onDismiss}
					aria-label={t("close", { ns: "common" })}
					data-testid="mcp-sessions-tools-pending-dismiss"
				>
					<X className="size-4" />
				</Button>
			)}
		</Alert>
	);
}