import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useCompleteOAuthFlowMutation, useLazyGetOAuthConfigStatusQuery } from "@/lib/store/apis/mcpApi";
import { AlertTriangle, CheckCircle2, ExternalLink, KeyRound, Loader2, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import i18n from "@/lib/i18n";
import { IconWrap, InfoBox, StepDots, UiVariant } from "./authorizerUi";

interface OAuth2AuthorizerProps {
	open: boolean;
	onClose: () => void;
	onSuccess: () => void;
	onError: (error: string) => void;
	onConflict?: (error: string) => void;
	authorizeUrl: string;
	oauthConfigId: string;
	// Flow row behind a reauthorize consent (from POST /reauthorize). Status
	// polls send it so the server answers from the flow's own state: the
	// config's bootstrap status has been "authorized" since the client was
	// first verified and never regresses, so polling it alone reads
	// "authorized" on the first tick, before the admin has signed in.
	flowId?: string;
	// The flow's deadline (from the same response). Polling stops with a
	// timeout once it passes instead of waiting on a flow row that the
	// server may already have swept.
	expiresAt?: string;
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
	// Runs when the admin clicks Retry after a failure, before the confirm
	// step reappears. A timed-out or denied flow is dead server-side (its
	// state is no longer honoured, its deadline has passed), so reopening the
	// popup on the same authorizeUrl can only fail again. The caller starts a
	// fresh flow here and swaps in the new authorizeUrl / flowId / expiresAt
	// through its own state. Without it Retry only resets local state.
	onRetry?: () => Promise<void>;
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
	flowId,
	expiresAt,
	isPerUserOauth,
	initialPopup,
	isReauthorize,
	onRetry,
}) => {
	const { t } = useTranslation("mcp");
	const { t: tc } = useTranslation("common");
	// Both auth types start on the confirm step and only open the popup from a
	// direct onClick: window.open() called from anywhere else (e.g. an effect
	// reacting to an async fetch resolving) loses the browser's "user
	// activation" and gets silently popup-blocked.
	const [status, setStatus] = useState<Status>("confirm");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	const [isRetrying, setIsRetrying] = useState(false);
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
			return t("registry.authorizer.oauth.providerFallback");
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

	const isPastDeadline = useCallback(() => {
		if (!expiresAt) return false;
		const deadline = Date.parse(expiresAt);
		return !Number.isNaN(deadline) && Date.now() > deadline;
	}, [expiresAt]);

	const checkOAuthStatus = useCallback(async () => {
		if (cancelledRef.current) return;
		if (isPastDeadline()) {
			handleOAuthFailed("Authorization timed out before the provider redirected back. Retry to start a new flow.");
			return;
		}
		try {
			const result = await getOAuthStatus({ oauthConfigId, flowId }).unwrap();
			if (cancelledRef.current) return;
			if (result.status === "authorized") {
				stopPolling();
				await handleOAuthComplete();
			} else if (result.status === "failed" || result.status === "expired") {
				handleOAuthFailed(i18n.t("registry.authorizer.oauth.authorizationStatus", { ns: "mcp", status: result.status }));
			}
		} catch (error) {
			console.error("Error checking OAuth status:", error);
		}
	}, [oauthConfigId, flowId, getOAuthStatus, stopPolling, handleOAuthComplete, handleOAuthFailed, isPastDeadline]);

	const startPolling = useCallback(() => {
		if (pollIntervalRef.current) clearInterval(pollIntervalRef.current);
		pollIntervalRef.current = setInterval(async () => {
			if (popupRef.current && popupRef.current.closed) {
				if (isPastDeadline()) {
					handleOAuthFailed("Authorization timed out before the provider redirected back. Retry to start a new flow.");
					return;
				}
				try {
					const result = await getOAuthStatus({ oauthConfigId, flowId }).unwrap();
					if (result.status === "authorized") {
						stopPolling();
						await handleOAuthComplete();
					} else if (result.status === "failed" || result.status === "expired") {
						stopPolling();
						handleOAuthFailed(i18n.t("registry.toast.authorizationFailed", { ns: "mcp" }));
					}
				} catch {
					// transient error — let polling continue
				}
				return;
			}
			await checkOAuthStatus();
		}, 2000);
	}, [checkOAuthStatus, getOAuthStatus, handleOAuthComplete, handleOAuthFailed, isPastDeadline, oauthConfigId, flowId, stopPolling]);

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
				handleOAuthFailed(event.data.error ?? i18n.t("registry.authorizer.oauth.flowFailed", { ns: "mcp" }));
			}
		};
		window.addEventListener("message", handleMessage);
		return () => window.removeEventListener("message", handleMessage);
	}, [checkOAuthStatus, handleOAuthFailed]);

	useEffect(() => {
		return () => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) popupRef.current.close();
		};
	}, [stopPolling]);

	const handleRetry = async () => {
		if (onRetry) {
			setIsRetrying(true);
			try {
				await onRetry();
			} catch (error) {
				// Stay on the failed step with the new reason; the old flow is
				// still dead, so falling through to confirm would just replay it.
				setErrorMessage(getErrorMessage(error));
				return;
			} finally {
				setIsRetrying(false);
			}
			if (cancelledRef.current) return;
		}
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
		confirm: isPerUserReauth ? t("registry.actions.refreshAdminCredential") : t("registry.authorizer.oauth.authorizeConnection"),
		polling: t("registry.authorizer.oauth.waiting"),
		blocked: t("registry.authorizer.oauth.popupBlocked"),
		success: t("registry.authorizer.oauth.connectionAuthorized"),
		failed: t("registry.toast.authorizationFailed"),
	};

	const subtitles: Record<Status, string> = {
		confirm: isPerUserReauth
			? t("registry.authorizer.oauth.confirmRenew")
			: t("registry.authorizer.oauth.confirmVerify"),
		polling: t("registry.authorizer.oauth.polling"),
		blocked: t("registry.authorizer.oauth.blocked"),
		success: t("registry.authorizer.oauth.success"),
		failed: t("registry.authorizer.oauth.failed"),
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
									<Trans
										t={t}
										i18nKey={isPerUserReauth ? "registry.authorizer.oauth.openHostRenew" : "registry.authorizer.oauth.openHostVerify"}
										values={{ host: authorizationHost }}
										components={{ strong: <strong /> }}
									/>
								</p>
								<p className="text-muted-foreground/80 text-xs">
									{isPerUserReauth ? t("registry.authorizer.oauth.renewHint") : t("registry.authorizer.oauth.verifyHint")}
								</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="per-user-oauth-cancel">
									{tc("cancel")}
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
								<p>{t("registry.authorizer.oauth.pollingBody")}</p>
								<p className="text-muted-foreground/80 text-xs">{t("registry.authorizer.oauth.keepPopupOpen")}</p>
							</InfoBox>
							<div className="flex items-center justify-between">
								<StepDots active={2} total={3} />
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-polling-cancel-btn">
									{tc("cancel")}
								</Button>
							</div>
						</>
					)}

					{/* Blocked */}
					{status === "blocked" && (
						<>
							<InfoBox variant="warning" icon={<AlertTriangle className="size-4" />}>
								<p>{t("registry.authorizer.oauth.blockedBody")}</p>
								<p className="text-xs opacity-80">{t("registry.authorizer.oauth.enablePopups")}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-pending-cancel-btn">
									{tc("cancel")}
								</Button>
								<Button size="sm" onClick={openPopup} data-testid="oauth-open-window-btn">
									<ExternalLink className="size-3.5" />
									{t("registry.authorizer.oauth.openAuthorization")}
								</Button>
							</div>
						</>
					)}

					{/* Success */}
					{status === "success" && (
						<InfoBox variant="success" icon={<CheckCircle2 className="size-4" />}>
							<p className="font-medium">{t("registry.authorizer.oauth.finishing")}</p>
							<p className="text-xs opacity-80">{t("registry.authorizer.oauth.closeBackground")}</p>
						</InfoBox>
					)}

					{/* Failed */}
					{status === "failed" && (
						<>
							<InfoBox variant="danger" icon={<XCircle className="size-4" />}>
								<p className="font-medium">{t("registry.authorizer.oauth.didNotComplete")}</p>
								<p className="text-xs opacity-80">{errorMessage ?? t("registry.authorizer.oauth.checkProvider")}</p>
							</InfoBox>
							<div className="flex justify-end gap-2">
								<Button size="sm" variant="outline" onClick={handleCancel} data-testid="oauth-failed-close-btn">
									{tc("close")}
								</Button>
								<Button size="sm" onClick={() => void handleRetry()} disabled={isRetrying} data-testid="oauth-failed-retry-btn">
									{isRetrying ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
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