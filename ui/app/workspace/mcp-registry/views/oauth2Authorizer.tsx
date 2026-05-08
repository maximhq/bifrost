import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import i18n from "@/lib/i18n";
import { useCompleteOAuthFlowMutation, useLazyGetOAuthConfigStatusQuery } from "@/lib/store/apis/mcpApi";
import { Loader2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

interface OAuth2AuthorizerProps {
	open: boolean;
	onClose: () => void;
	onSuccess: () => void;
	onError: (error: string) => void;
	authorizeUrl: string;
	oauthConfigId: string;
	mcpClientId: string;
	isPerUserOauth?: boolean;
}

export const OAuth2Authorizer: React.FC<OAuth2AuthorizerProps> = ({
	open,
	onClose,
	onSuccess,
	onError,
	authorizeUrl,
	oauthConfigId,
	isPerUserOauth,
}) => {
	const [status, setStatus] = useState<"confirm" | "pending" | "polling" | "success" | "failed">(isPerUserOauth ? "confirm" : "pending");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	const popupRef = useRef<Window | null>(null);
	const pollIntervalRef = useRef<NodeJS.Timeout | null>(null);
	const isCompletingRef = useRef(false);
	// Set to true when the user cancels so in-flight async callbacks do not
	// invoke onSuccess / onError / onClose after the dialog is dismissed.
	const cancelledRef = useRef(false);

	// RTK Query hooks
	const [getOAuthStatus] = useLazyGetOAuthConfigStatusQuery();
	const [completeOAuth] = useCompleteOAuthFlowMutation();

	// Stop polling
	const stopPolling = useCallback(() => {
		if (pollIntervalRef.current) {
			clearInterval(pollIntervalRef.current);
			pollIntervalRef.current = null;
		}
	}, []);

	// Handle successful OAuth completion
	const handleOAuthComplete = useCallback(async () => {
		if (cancelledRef.current) return;
		// Guard against concurrent calls (race between postMessage and polling)
		if (isCompletingRef.current) return;
		isCompletingRef.current = true;

		// Close popup if still open
		if (popupRef.current && !popupRef.current.closed) {
			popupRef.current.close();
		}

		// Call complete-oauth endpoint using RTK Query mutation
		// Use oauthConfigId instead of mcpClientId for multi-instance support
		try {
			await completeOAuth(oauthConfigId).unwrap();
			if (cancelledRef.current) return;
			setStatus("success");
			onSuccess();
			setTimeout(() => {
				if (!cancelledRef.current) onClose();
			}, 1000);
		} catch (error) {
			if (cancelledRef.current) return;
			const errMsg = getErrorMessage(error);
			setStatus("failed");
			setErrorMessage(errMsg);
			onError(errMsg);
		}
	}, [oauthConfigId, completeOAuth, onSuccess, onClose, onError]);

	// Handle OAuth failure
	const handleOAuthFailed = useCallback(
		(reason: string) => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) {
				popupRef.current.close();
			}
			if (cancelledRef.current) return;
			setStatus("failed");
			setErrorMessage(reason);
			onError(reason);
		},
		[stopPolling, onError],
	);

	// Check OAuth status (called by postMessage or polling)
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

	// Poll OAuth status
	const startPolling = useCallback(() => {
		// Clear any existing interval
		if (pollIntervalRef.current) {
			clearInterval(pollIntervalRef.current);
		}

		pollIntervalRef.current = setInterval(async () => {
			// Check if popup is still open
			if (popupRef.current && popupRef.current.closed) {
				// Popup closed - check status before assuming cancellation
				// (OAuth callback page closes the popup after success)
				try {
					const result = await getOAuthStatus(oauthConfigId).unwrap();
					if (result.status === "authorized") {
						stopPolling();
						await handleOAuthComplete();
					} else if (result.status === "failed" || result.status === "expired") {
						stopPolling();
						handleOAuthFailed("Authorization failed");
					}
					// pending or other non-terminal: let polling continue
				} catch {
					// transient fetch error: let polling continue
				}
				return;
			}

			await checkOAuthStatus();
		}, 2000); // Poll every 2 seconds
	}, [checkOAuthStatus, handleOAuthFailed]);

	// Open popup and start polling
	const openPopup = useCallback(() => {
		// Reset completion and cancelled guards for each fresh OAuth attempt
		isCompletingRef.current = false;
		cancelledRef.current = false;

		// Close any existing popup
		if (popupRef.current && !popupRef.current.closed) {
			popupRef.current.close();
		}

		// Open OAuth popup
		const width = 600;
		const height = 700;
		const left = window.screen.width / 2 - width / 2;
		const top = window.screen.height / 2 - height / 2;

		popupRef.current = window.open(
			authorizeUrl,
			"oauth_popup",
			`width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes`,
		);

		setStatus("polling");

		// Start polling OAuth status
		startPolling();
	}, [authorizeUrl, startPolling]);

	// Listen for postMessage from OAuth callback popup
	useEffect(() => {
		const handleMessage = (event: MessageEvent) => {
			// Verify message is from OAuth callback
			if (event.data?.type === "oauth_success") {
				// Trigger immediate status check; stopPolling is called inside
				// checkOAuthStatus only after a confirmed terminal state, so
				// transient fetch errors still allow polling to continue.
				checkOAuthStatus();
			}
		};

		window.addEventListener("message", handleMessage);
		return () => {
			window.removeEventListener("message", handleMessage);
		};
	}, [checkOAuthStatus]);

	// Open popup when dialog opens (skip if waiting for user confirmation)
	useEffect(() => {
		if (open && status === "pending") {
			openPopup();
		}
	}, [open, status, openPopup]);

	// Handle user confirming per-user OAuth test
	const handleConfirmPerUserOAuth = () => {
		setStatus("pending");
		openPopup();
	};

	// Cleanup on unmount
	useEffect(() => {
		return () => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) {
				popupRef.current.close();
			}
		};
	}, [stopPolling]);

	const handleRetry = () => {
		setErrorMessage(null);
		isCompletingRef.current = false;
		if (isPerUserOauth) {
			setStatus("confirm");
		} else {
			setStatus("pending");
			openPopup();
		}
	};

	const handleCancel = () => {
		cancelledRef.current = true;
		stopPolling();
		isCompletingRef.current = false;
		if (popupRef.current && !popupRef.current.closed) {
			popupRef.current.close();
		}
		onClose();
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(nextOpen) => {
				if (!nextOpen) {
					handleCancel();
				}
			}}
		>
			<DialogContent
				className="sm:max-w-md"
				onPointerDownOutside={(e) => {
					e.preventDefault();
					handleCancel();
				}}
				onEscapeKeyDown={(e) => {
					e.preventDefault();
					handleCancel();
				}}
			>
				<DialogHeader>
					<DialogTitle>
						{status === "confirm" ? i18n.t("workspace.oauth.testOAuthConfiguration") : i18n.t("workspace.oauth.oAuthAuthorization")}
					</DialogTitle>
					<DialogDescription>
						{status === "confirm" && i18n.t("workspace.oauth.oneTimeLoginNeeded")}
						{status === "pending" && i18n.t("workspace.oauth.openingAuthorizationWindow")}
						{status === "polling" && i18n.t("workspace.oauth.waitingForAuthorization")}
						{status === "success" && i18n.t("workspace.oauth.authorizationSuccessful")}
						{status === "failed" && i18n.t("workspace.oauth.authorizationFailed")}
					</DialogDescription>
				</DialogHeader>

				<div className="flex flex-col items-center justify-center space-y-4">
					{status === "confirm" && (
						<>
							<div className="text-muted-foreground space-y-3 text-sm">
								<p>{i18n.t("workspace.oauth.toSetUpMCP")}</p>
								<p dangerouslySetInnerHTML={{ __html: i18n.t("workspace.oauth.oneTimeTest") }} />
								<p>{i18n.t("workspace.oauth.eachUserAuthenticates")}</p>
							</div>
							<div className="flex w-full justify-end space-x-2">
								<Button onClick={handleCancel} variant="outline" data-testid="per-user-oauth-cancel">
									{i18n.t("workspace.oauth.cancel")}
								</Button>
								<Button onClick={handleConfirmPerUserOAuth} data-testid="per-user-oauth-confirm">
									{i18n.t("workspace.oauth.continueWithTestLogin")}
								</Button>
							</div>
						</>
					)}

					{status === "polling" && (
						<>
							<Loader2 className="text-secondary-foreground h-4 w-4 animate-spin" />
							<p className="text-muted-foreground text-sm">{i18n.t("workspace.oauth.completeAuthorizationInPopup")}</p>
						</>
					)}

					{status === "success" && (
						<>
							<div className="flex h-12 w-12 items-center justify-center rounded-full bg-green-100">
								<svg className="h-6 w-6 text-green-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" />
								</svg>
							</div>
							<p className="text-sm text-green-600">{i18n.t("workspace.oauth.mcpServerConnected")}</p>
						</>
					)}

					{status === "failed" && (
						<>
							<div className="flex h-12 w-12 items-center justify-center rounded-full bg-red-100">
								<svg className="h-6 w-6 text-red-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
								</svg>
							</div>
							<p className="text-sm text-red-600">{errorMessage || i18n.t("common.error")}</p>
							<Button onClick={handleRetry} variant="outline">
								{i18n.t("workspace.oauth.retry")}
							</Button>
						</>
					)}
				</div>

				{status === "polling" && (
					<div className="flex justify-end space-x-2">
						<Button onClick={handleCancel} variant="outline">
							{i18n.t("workspace.oauth.cancel")}
						</Button>
					</div>
				)}
			</DialogContent>
		</Dialog>
	);
};