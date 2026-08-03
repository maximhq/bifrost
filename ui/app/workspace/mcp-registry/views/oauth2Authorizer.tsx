import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useCompleteOAuthFlowMutation, useLazyGetOAuthConfigStatusQuery } from "@/lib/store/apis/mcpApi";
import { cn } from "@/lib/utils";
import { AlertTriangle, CheckCircle2, ExternalLink, KeyRound, Loader2, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface OAuth2AuthorizerProps {
	open: boolean;
	onClose: () => void;
	onSuccess: () => void;
	onError: (error: string) => void;
	onConflict?: (error: string) => void;
	authorizeUrl: string;
	oauthConfigId: string;
	mcpClientId: string;
	isPerUserOauth?: boolean;
}

type Status = "confirm" | "polling" | "blocked" | "success" | "failed";

// ── Icon slot ────────────────────────────────────────────────────────────────

function IconWrap({ status }: { status: Status }) {
	const base = "flex size-9 shrink-0 items-center justify-center rounded-md";

	if (status === "polling") {
		return (
			<div className={cn(base, "bg-blue-50 text-blue-600 dark:bg-blue-950 dark:text-blue-300")}>
				<Loader2 className="size-4 animate-spin" />
			</div>
		);
	}
	if (status === "success") {
		return (
			<div className={cn(base, "bg-green-50 text-green-700 dark:bg-green-950 dark:text-green-300")}>
				<CheckCircle2 className="size-4" />
			</div>
		);
	}
	if (status === "failed") {
		return (
			<div className={cn(base, "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300")}>
				<XCircle className="size-4" />
			</div>
		);
	}
	if (status === "blocked") {
		return (
			<div className={cn(base, "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300")}>
				<AlertTriangle className="size-4" />
			</div>
		);
	}
	// confirm (default)
	return (
		<div className={cn(base, "bg-muted text-muted-foreground")}>
			<ShieldCheck className="size-4" />
		</div>
	);
}

// ── Info box ──────────────────────────────────────────────────────────────────

function InfoBox({
	variant = "default",
	icon,
	children,
}: {
	variant?: "default" | "success" | "danger" | "warning";
	icon: React.ReactNode;
	children: React.ReactNode;
}) {
	return (
		<div
			className={cn("flex gap-3 rounded-md border p-3.5 text-sm", {
				"border-border bg-muted/40 text-muted-foreground": variant === "default",
				"border-green-200/60 bg-green-50/70 text-green-800 dark:border-green-800/40 dark:bg-green-950/40 dark:text-green-200":
					variant === "success",
				"border-red-200/60 bg-red-50/70 text-red-800 dark:border-red-800/40 dark:bg-red-950/40 dark:text-red-200": variant === "danger",
				"border-amber-200/60 bg-amber-50/70 text-amber-800 dark:border-amber-800/40 dark:bg-amber-950/40 dark:text-amber-200":
					variant === "warning",
			})}
		>
			<span className="mt-0.5 shrink-0">{icon}</span>
			<div className="space-y-1 leading-relaxed">{children}</div>
		</div>
	);
}

// ── Step dots ─────────────────────────────────────────────────────────────────

function StepDots({ active, total }: { active: number; total: number }) {
	return (
		<div className="flex items-center gap-1">
			{Array.from({ length: total }).map((_, i) => (
				<div key={i} className={cn("size-1.5 rounded-full transition-colors", i < active ? "bg-blue-500" : "bg-border")} />
			))}
		</div>
	);
}

// ── Main component ────────────────────────────────────────────────────────────

export const OAuth2Authorizer: React.FC<OAuth2AuthorizerProps> = ({
	open,
	onClose,
	onSuccess,
	onError,
	onConflict,
	authorizeUrl,
	oauthConfigId,
	isPerUserOauth,
}) => {
	const [status, setStatus] = useState<Status>(isPerUserOauth ? "confirm" : "polling");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	const popupRef = useRef<Window | null>(null);
	const pollIntervalRef = useRef<NodeJS.Timeout | null>(null);
	const isCompletingRef = useRef(false);
	const cancelledRef = useRef(false);

	const [getOAuthStatus] = useLazyGetOAuthConfigStatusQuery();
	const [completeOAuth] = useCompleteOAuthFlowMutation();

	const authorizationHost = useMemo(() => {
		try {
			return new URL(authorizeUrl).host;
		} catch {
			return "the OAuth provider";
		}
	}, [authorizeUrl]);

	const stopPolling = useCallback(() => {
		if (pollIntervalRef.current) {
			clearInterval(pollIntervalRef.current);
			pollIntervalRef.current = null;
		}
	}, []);

	const handleOAuthComplete = useCallback(async () => {
		if (cancelledRef.current || isCompletingRef.current) return;
		isCompletingRef.current = true;
		if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
		try {
			await completeOAuth(oauthConfigId).unwrap();
			if (cancelledRef.current) return;
			setStatus("success");
			onSuccess();
		} catch (error) {
			if (cancelledRef.current) return;
			const errMsg = getErrorMessage(error);
			if ((error as any)?.status === 409 && onConflict) {
				setStatus(isPerUserOauth ? "confirm" : "polling");
				setErrorMessage(null);
				isCompletingRef.current = false;
				onConflict(errMsg);
				return;
			}
			setStatus("failed");
			setErrorMessage(errMsg);
			onError(errMsg);
		}
	}, [oauthConfigId, completeOAuth, onSuccess, onError, onConflict, isPerUserOauth]);

	const handleOAuthFailed = useCallback(
		(reason: string) => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
			if (cancelledRef.current) return;
			setStatus("failed");
			setErrorMessage(reason);
			onError(reason);
		},
		[stopPolling, onError],
	);

	const checkOAuthStatus = useCallback(async () => {
		if (cancelledRef.current) return;
		try {
			const result = await getOAuthStatus(oauthConfigId).unwrap();
			if (cancelledRef.current) return;
			if (result.status === "authorized") {
				stopPolling();
				await handleOAuthComplete();
			} else if (result.status === "failed" || result.status === "expired") {
				handleOAuthFailed(`Authorization ${result.status}`);
			}
		} catch (error) {
			console.error("Error checking OAuth status:", error);
		}
	}, [oauthConfigId, getOAuthStatus, stopPolling, handleOAuthComplete, handleOAuthFailed]);

	const startPolling = useCallback(() => {
		if (pollIntervalRef.current) clearInterval(pollIntervalRef.current);
		pollIntervalRef.current = setInterval(async () => {
			if (popupRef.current && popupRef.current.closed) {
				try {
					const result = await getOAuthStatus(oauthConfigId).unwrap();
					if (result.status === "authorized") {
						stopPolling();
						await handleOAuthComplete();
					} else if (result.status === "failed" || result.status === "expired") {
						stopPolling();
						handleOAuthFailed("Authorization failed");
					}
				} catch {
					// transient error — let polling continue
				}
				return;
			}
			await checkOAuthStatus();
		}, 2000);
	}, [checkOAuthStatus, getOAuthStatus, handleOAuthComplete, handleOAuthFailed, oauthConfigId, stopPolling]);

	const openPopup = useCallback(() => {
		isCompletingRef.current = false;
		cancelledRef.current = false;
		if (popupRef.current && !popupRef.current.closed) popupRef.current.close();

		const width = 600;
		const height = 700;
		const left = window.screen.width / 2 - width / 2;
		const top = window.screen.height / 2 - height / 2;
		const popup = window.open(
			authorizeUrl,
			"oauth_popup",
			`width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes`,
		);

		if (!popup || popup.closed) {
			popupRef.current = null;
			setStatus("blocked");
			return;
		}

		popupRef.current = popup;
		setStatus("polling");
		startPolling();
	}, [authorizeUrl, startPolling]);

	useEffect(() => {
		const handleMessage = (event: MessageEvent) => {
			if (event.source !== popupRef.current || event.origin !== window.location.origin) return;
			if (event.data?.type === "oauth_success") {
				void checkOAuthStatus();
				return;
			}
			if (event.data?.type === "oauth_failed") {
				handleOAuthFailed(event.data.error ?? "OAuth flow failed");
			}
		};
		window.addEventListener("message", handleMessage);
		return () => window.removeEventListener("message", handleMessage);
	}, [checkOAuthStatus, handleOAuthFailed]);

	// Auto-open popup for non-per-user OAuth flows.
	const openPopupRef = useRef(openPopup);
	useEffect(() => {
		openPopupRef.current = openPopup;
	});
	useEffect(() => {
		if (open && !isPerUserOauth) openPopupRef.current();
	}, [open, isPerUserOauth]);

	useEffect(() => {
		return () => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
		};
	}, [stopPolling]);

	const handleRetry = () => {
		setErrorMessage(null);
		isCompletingRef.current = false;
		setStatus(isPerUserOauth ? "confirm" : "polling");
		if (!isPerUserOauth) openPopup();
	};

	const handleCancel = () => {
		cancelledRef.current = true;
		stopPolling();
		isCompletingRef.current = false;
		if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
		onClose();
	};

	const titles: Record<Status, string> = {
		confirm: "Authorize connection",
		polling: "Waiting for authorization",
		blocked: "Popup blocked",
		success: "Connection authorized",
		failed: "Authorization failed",
	};

	const subtitles: Record<Status, string> = {
		confirm: "Run a one-time OAuth test before enabling this server.",
		polling: "Complete sign-in in the popup window to continue.",
		blocked: "Allow popups for this site, then try again.",
		success: "OAuth authorization completed successfully.",
		failed: "The OAuth flow did not complete.",
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next) handleCancel();
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
						<IconWrap status={status} />
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
									We'll open <strong>{authorizationHost}</strong> to verify the OAuth setup and discover available tools.
								</p>
								<p className="text-muted-foreground/80 text-xs">
									This login is for setup only. Each user authenticates individually when they connect.
								</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="per-user-oauth-cancel">
									Cancel
								</Button>
								<Button size="sm" onClick={openPopup} data-testid="per-user-oauth-confirm">
									<ExternalLink className="size-3.5" />
									Continue
								</Button>
							</div>
						</>
					)}

					{/* Polling */}
					{status === "polling" && (
						<>
							<InfoBox icon={<Loader2 className="size-4 animate-spin" />}>
								<p>This dialog will update automatically once the provider redirects back.</p>
								<p className="text-muted-foreground/80 text-xs">Keep the popup open until authorization is complete.</p>
							</InfoBox>
							<div className="flex items-center justify-between">
								<StepDots active={2} total={3} />
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-polling-cancel-btn">
									Cancel
								</Button>
							</div>
						</>
					)}

					{/* Blocked */}
					{status === "blocked" && (
						<>
							<InfoBox variant="warning" icon={<AlertTriangle className="size-4" />}>
								<p>Your browser prevented the authorization window from opening.</p>
								<p className="text-xs opacity-80">Enable popups for this site in your browser settings, then try again.</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-pending-cancel-btn">
									Cancel
								</Button>
								<Button size="sm" onClick={openPopup} data-testid="oauth-open-window-btn">
									<ExternalLink className="size-3.5" />
									Open authorization
								</Button>
							</div>
						</>
					)}

					{/* Success */}
					{status === "success" && (
						<InfoBox variant="success" icon={<CheckCircle2 className="size-4" />}>
							<p className="font-medium">Finishing setup and syncing available tools.</p>
							<p className="text-xs opacity-80">You can close this dialog; setup will complete in the background.</p>
						</InfoBox>
					)}

					{/* Failed */}
					{status === "failed" && (
						<>
							<InfoBox variant="danger" icon={<XCircle className="size-4" />}>
								<p className="font-medium">Authorization did not complete.</p>
								<p className="text-xs opacity-80">{errorMessage ?? "Check your OAuth provider configuration or try again."}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-failed-close-btn">
									Close
								</Button>
								<Button size="sm" onClick={handleRetry} data-testid="oauth-failed-retry-btn">
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