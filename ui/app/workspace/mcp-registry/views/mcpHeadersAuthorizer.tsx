// MCPHeadersAuthorizer mirrors OAuth2Authorizer's dialog chrome for the
// per-user-headers create/refresh flow: same bordered icon header, same
// InfoBox body copy, same Cancel / Continue footer, built from the shared
// pieces in authorizerUi.tsx. The divergence is that the verify step is a
// values form filled inline (no upstream redirect/popup), so there's an
// extra "input" step between confirm and testing, and the create call is a
// single POST /api/mcp/client where the server runs verify + discover +
// persist atomically. Mirrors per-user OAuth where the admin's temp access
// token plays the analogous role.

import HeadersForm from "@/components/headersForm";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getErrorMessage } from "@/lib/store";
import { CheckCircle2, KeyRound, Loader2, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import React, { useEffect, useRef, useState } from "react";
import { IconWrap, InfoBox, StepDots, UiVariant } from "./authorizerUi";

interface MCPHeadersAuthorizerProps {
	open: boolean;
	onClose: () => void;
	onSuccess: () => void;
	onError: (error: string) => void;
	onConflict?: (error: string) => void;
	// Required key schema, rendered as the form's input fields.
	perUserHeaderKeys: string[];
	// Called with the collected sample values once the admin submits the
	// form. The handler decides what endpoint to hit (Create flow's
	// /api/mcp/client vs bootstrap's /api/mcp/client/{id}/verify-headers).
	// Throw to surface a failure in the dialog.
	submitHandler: (values: Record<string, string>) => Promise<void>;
}

type Status = "confirm" | "input" | "testing" | "success" | "failed";

const STATUS_ICON: Record<Status, { variant: UiVariant; icon: React.ReactNode }> = {
	confirm: { variant: "muted", icon: <ShieldCheck className="size-4" /> },
	input: { variant: "muted", icon: <KeyRound className="size-4" /> },
	testing: { variant: "info", icon: <Loader2 className="size-4 animate-spin" /> },
	success: { variant: "success", icon: <CheckCircle2 className="size-4" /> },
	failed: { variant: "danger", icon: <XCircle className="size-4" /> },
};

const titles: Record<Status, string> = {
	confirm: "Test header configuration",
	input: "Enter sample values",
	testing: "Verifying connection",
	success: "Connection verified",
	failed: "Verification failed",
};

const subtitles: Record<Status, string> = {
	confirm: "Verify your header setup to discover available tools.",
	input: "Enter sample values to verify the connection.",
	testing: "Checking your headers and discovering available tools.",
	success: "Header verification completed successfully.",
	failed: "The verification did not complete.",
};

export const MCPHeadersAuthorizer: React.FC<MCPHeadersAuthorizerProps> = ({
	open,
	onClose,
	onSuccess,
	onError,
	onConflict,
	perUserHeaderKeys,
	submitHandler,
}) => {
	const [status, setStatus] = useState<Status>("confirm");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	// Set to true when the user cancels so in-flight async callbacks do not
	// invoke onSuccess / onError / onClose after the dialog is dismissed.
	const cancelledRef = useRef(false);

	// Reset state every time the dialog opens so a retry from a previous
	// session doesn't carry over.
	useEffect(() => {
		if (open) {
			setStatus("confirm");
			setErrorMessage(null);
			cancelledRef.current = false;
		}
	}, [open]);

	const handleConfirm = () => {
		setStatus("input");
	};

	const handleRunTest = async (values: Record<string, string>) => {
		if (cancelledRef.current) return;
		setStatus("testing");
		try {
			await submitHandler(values);
			if (cancelledRef.current) return;
			setStatus("success");
			onSuccess();
		} catch (err) {
			if (cancelledRef.current) return;
			const errMsg = getErrorMessage(err);
			if ((err as any)?.status === 409) {
				setStatus("input");
				setErrorMessage(null);
				onConflict?.(errMsg);
				return;
			}
			setStatus("failed");
			setErrorMessage(errMsg);
			onError(errMsg);
		}
	};

	const handleRetry = () => {
		setErrorMessage(null);
		setStatus("input");
	};

	const handleCancel = () => {
		cancelledRef.current = true;
		onClose();
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) handleCancel();
			}}
		>
			<DialogContent
				className="gap-0 overflow-hidden p-0 sm:max-w-md"
				onPointerDownOutside={(e) => {
					e.preventDefault();
					handleCancel();
				}}
				onEscapeKeyDown={(e) => {
					e.preventDefault();
					handleCancel();
				}}
			>
				{/* Header */}
				<DialogHeader className="border-b px-5 py-4 text-left">
					<div className="flex items-start gap-3">
						<IconWrap variant={STATUS_ICON[status].variant} icon={STATUS_ICON[status].icon} />
						<div className="min-w-0 space-y-0.5">
							<DialogTitle className="text-sm leading-snug font-medium">{titles[status]}</DialogTitle>
							<DialogDescription className="text-xs leading-relaxed">{subtitles[status]}</DialogDescription>
						</div>
					</div>
				</DialogHeader>

				{/* Body */}
				<div className="space-y-3 px-5 py-4">
					{/* Confirm */}
					{status === "confirm" && (
						<>
							<InfoBox icon={<KeyRound className="size-4" />}>
								<p>
									To set up this MCP server, we need to verify that your header configuration is correct and discover the
									available tools.
								</p>
								<p className="text-muted-foreground/80 text-xs">
									You will be asked to provide sample values for the required headers. Bifrost keeps these values on file to
									periodically refresh the available tool list; they are never used for real end-user requests. Once verified,
									each user will submit their own header values when they use this MCP server.
								</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="per-user-headers-cancel">
									Cancel
								</Button>
								<Button size="sm" onClick={handleConfirm} data-testid="per-user-headers-confirm">
									Continue
								</Button>
							</div>
						</>
					)}

					{/* Input */}
					{status === "input" && (
						<>
							<InfoBox icon={<KeyRound className="size-4" />}>
								<p>
									These values verify the connection now and are kept on file so Bifrost can periodically refresh the available
									tool list. Each user still submits their own values when they use this server.
								</p>
							</InfoBox>
							<HeadersForm
								requiredKeys={perUserHeaderKeys}
								onSubmit={handleRunTest}
								submitLabel="Run Test"
								onCancel={handleCancel}
								testIdPrefix="per-user-headers-admin-test"
							/>
						</>
					)}

					{/* Testing */}
					{status === "testing" && (
						<>
							<InfoBox icon={<Loader2 className="size-4 animate-spin" />}>
								<p>Checking your headers against the server and discovering available tools.</p>
								<p className="text-muted-foreground/80 text-xs">This only takes a moment.</p>
							</InfoBox>
							<div className="flex items-center justify-end">
								<StepDots active={2} total={3} />
							</div>
						</>
					)}

					{/* Success */}
					{status === "success" && (
						<InfoBox variant="success" icon={<CheckCircle2 className="size-4" />}>
							<p className="font-medium">Header configuration verified.</p>
							<p className="text-xs opacity-80">You can close this dialog.</p>
						</InfoBox>
					)}

					{/* Failed */}
					{status === "failed" && (
						<>
							<InfoBox variant="danger" icon={<XCircle className="size-4" />}>
								<p className="font-medium">Verification did not complete.</p>
								<p className="text-xs opacity-80">{errorMessage ?? "Check your header values and try again."}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="mcp-headers-authorizer-close-btn">
									Close
								</Button>
								<Button size="sm" onClick={handleRetry} data-testid="mcp-headers-authorizer-retry-btn">
									<RefreshCw className="size-3.5" />
									Retry
								</Button>
							</div>
						</>
					)}
				</div>
			</DialogContent>
		</Dialog>
	);
};
