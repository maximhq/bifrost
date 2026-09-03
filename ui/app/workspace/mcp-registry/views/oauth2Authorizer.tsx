import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useCompleteOAuthFlowMutation, useLazyGetOAuthConfigStatusQuery } from "@/lib/store/apis/mcpApi";
import { AlertTriangle, CheckCircle2, ExternalLink, KeyRound, Loader2, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { IconWrap, InfoBox, StepDots, UiVariant } from "./authorizerUi";

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
	// A popup the caller already opened synchronously (before any await), to
	// preserve the triggering click's transient user-activation. When
	// present, openPopup navigates this handle instead of calling
	// window.open itself — a fresh window.open after an awaited network
	// round-trip risks the browser blocking it outright.
	initialPopup?: Window | null;
	// True when this dialog is redoing consent for an already-verified client
	// (the "Refresh admin credential" action), as opposed to the first-time
	// bootstrap verification. Only affects the confirm-step copy.
	isReauthorize?: boolean;
}

type Status = "confirm" | "polling" | "blocked" | "success" | "failed";

const STATUS_ICON: Record<Status, { variant: UiVariant; icon: React.ReactNode }> = {
	confirm: { variant: "muted", icon: <ShieldCheck className="size-4" /> },
	polling: { variant: "info", icon: <Loader2 className="size-4 animate-spin" /> },
	blocked: { variant: "warning", icon: <AlertTriangle className="size-4" /> },
	success: { variant: "success", icon: <CheckCircle2 className="size-4" /> },
	failed: { variant: "danger", icon: <XCircle className="size-4" /> },
};

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
	initialPopup,
	isReauthorize,
}) => {
	const t = useT();
	// Both auth types start on the confirm step and only open the popup from a
	// direct onClick: window.open() called from anywhere else (e.g. an effect
	// reacting to an async fetch resolving) loses the browser's "user
	// activation" and gets silently popup-blocked.
	const [status, setStatus] = useState<Status>("confirm");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	const popupRef = useRef<Window | null>(null);
	const pollIntervalRef = useRef<NodeJS.Timeout | null>(null);
	const isCompletingRef = useRef(false);
	const cancelledRef = useRef(false);
	// initialPopup is only good for one use — a retry must open a fresh
	// window rather than re-navigating a handle already spent on a prior
	// (blocked or failed) attempt.
	const initialPopupConsumedRef = useRef(false);

	const [getOAuthStatus] = useLazyGetOAuthConfigStatusQuery();
	const [completeOAuth] = useCompleteOAuthFlowMutation();

	const authorizationHost = useMemo(() => {
		try {
			return new URL(authorizeUrl).host;
		} catch {
			return t("mcp.oauth.providerFallback");
		}
	}, [authorizeUrl, t]);

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
				setStatus("confirm");
				setErrorMessage(null);
				isCompletingRef.current = false;
				onConflict(errMsg);
				return;
			}
			setStatus("failed");
			setErrorMessage(errMsg);
			onError(errMsg);
		}
	}, [oauthConfigId, completeOAuth, onSuccess, onError, onConflict]);

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
				handleOAuthFailed(t("mcp.oauth.authorizationStatus", { status: result.status }));
			}
		} catch (error) {
			console.error("Error checking OAuth status:", error);
		}
	}, [oauthConfigId, getOAuthStatus, stopPolling, handleOAuthComplete, handleOAuthFailed, t]);

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
						handleOAuthFailed(t("mcp.oauth.authorizationFailedShort"));
					}
				} catch {
					// transient error — let polling continue
				}
				return;
			}
			await checkOAuthStatus();
		}, 2000);
	}, [checkOAuthStatus, getOAuthStatus, handleOAuthComplete, handleOAuthFailed, oauthConfigId, stopPolling, t]);

	const openPopup = useCallback(() => {
		isCompletingRef.current = false;
		cancelledRef.current = false;
		if (popupRef.current && !popupRef.current.closed) popupRef.current.close();

		const width = 600;
		const height = 700;
		const left = window.screen.width / 2 - width / 2;
		const top = window.screen.height / 2 - height / 2;

		let popup: Window | null = null;
		if (!initialPopupConsumedRef.current && initialPopup && !initialPopup.closed) {
			initialPopupConsumedRef.current = true;
			popup = initialPopup;
			try {
				popup.location.href = authorizeUrl;
			} catch {
				popup = null;
			}
		} else {
			popup = window.open(
				authorizeUrl,
				"oauth_popup",
				`width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes`,
			);
		}

		if (!popup || popup.closed) {
			popupRef.current = null;
			setStatus("blocked");
			return;
		}

		popupRef.current = popup;
		setStatus("polling");
		startPolling();
	}, [authorizeUrl, startPolling, initialPopup]);

	useEffect(() => {
		const handleMessage = (event: MessageEvent) => {
			if (event.source !== popupRef.current || event.origin !== window.location.origin) return;
			if (event.data?.type === "oauth_success") {
				void checkOAuthStatus();
				return;
			}
			if (event.data?.type === "oauth_failed") {
				handleOAuthFailed(event.data.error ?? t("mcp.oauth.flowFailed"));
			}
		};
		window.addEventListener("message", handleMessage);
		return () => window.removeEventListener("message", handleMessage);
	}, [checkOAuthStatus, handleOAuthFailed, t]);

	useEffect(() => {
		return () => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
		};
	}, [stopPolling]);

	const handleRetry = () => {
		setErrorMessage(null);
		isCompletingRef.current = false;
		setStatus("confirm");
	};

	const handleCancel = () => {
		cancelledRef.current = true;
		stopPolling();
		isCompletingRef.current = false;
		if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
		onClose();
	};

	const isPerUserReauth = isPerUserOauth && isReauthorize;

	const titles: Record<Status, string> = {
		confirm: isPerUserReauth ? t("mcp.oauth.refreshAdmin") : t("mcp.oauth.authorizeConnection"),
		polling: t("mcp.oauth.waiting"),
		blocked: t("mcp.oauth.popupBlocked"),
		success: t("mcp.oauth.connectionAuthorized"),
		failed: t("mcp.oauth.authorizationFailed"),
	};

	const subtitles: Record<Status, string> = {
		confirm: isPerUserReauth ? t("mcp.oauth.confirmReauth") : t("mcp.oauth.confirmFirst"),
		polling: t("mcp.oauth.polling"),
		blocked: t("mcp.oauth.blocked"),
		success: t("mcp.oauth.success"),
		failed: t("mcp.oauth.failed"),
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
									{isPerUserReauth
										? t("mcp.oauth.openRenew", { host: authorizationHost })
										: t("mcp.oauth.openVerify", { host: authorizationHost })}
								</p>
								<p className="text-muted-foreground/80 text-xs">{isPerUserReauth ? t("mcp.oauth.noteReauth") : t("mcp.oauth.noteFirst")}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="per-user-oauth-cancel">
									{t("common.cancel")}
								</Button>
								<Button size="sm" onClick={openPopup} data-testid="per-user-oauth-confirm">
									<ExternalLink className="size-3.5" />
									{t("common.continue")}
								</Button>
							</div>
						</>
					)}

					{/* Polling */}
					{status === "polling" && (
						<>
							<InfoBox icon={<Loader2 className="size-4 animate-spin" />}>
								<p>{t("mcp.oauth.pollingBody")}</p>
								<p className="text-muted-foreground/80 text-xs">{t("mcp.oauth.pollingHint")}</p>
							</InfoBox>
							<div className="flex items-center justify-between">
								<StepDots active={2} total={3} />
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-polling-cancel-btn">
									{t("common.cancel")}
								</Button>
							</div>
						</>
					)}

					{/* Blocked */}
					{status === "blocked" && (
						<>
							<InfoBox variant="warning" icon={<AlertTriangle className="size-4" />}>
								<p>{t("mcp.oauth.blockedBody")}</p>
								<p className="text-xs opacity-80">{t("mcp.oauth.blockedHint")}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-pending-cancel-btn">
									{t("common.cancel")}
								</Button>
								<Button size="sm" onClick={openPopup} data-testid="oauth-open-window-btn">
									<ExternalLink className="size-3.5" />
									{t("mcp.oauth.openAuthorization")}
								</Button>
							</div>
						</>
					)}

					{/* Success */}
					{status === "success" && (
						<InfoBox variant="success" icon={<CheckCircle2 className="size-4" />}>
							<p className="font-medium">{t("mcp.oauth.successBody")}</p>
							<p className="text-xs opacity-80">{t("mcp.oauth.successHint")}</p>
						</InfoBox>
					)}

					{/* Failed */}
					{status === "failed" && (
						<>
							<InfoBox variant="danger" icon={<XCircle className="size-4" />}>
								<p className="font-medium">{t("mcp.oauth.failedBody")}</p>
								<p className="text-xs opacity-80">{errorMessage ?? t("mcp.oauth.failedFallback")}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-failed-close-btn">
									{t("common.close")}
								</Button>
								<Button size="sm" onClick={handleRetry} data-testid="oauth-failed-retry-btn">
									<RefreshCw className="size-3.5" />
									{t("common.retry")}
								</Button>
							</div>
						</>
					)}
				</div>
			</DialogContent>
		</Dialog>
	);
};