"use client";

import { useEffect, useRef, useState } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Loader2 } from "lucide-react";

interface OAuth2AuthorizerProps {
	open: boolean;
	onClose: () => void;
	onSuccess: () => void;
	onError: (error: string) => void;
	authorizeUrl: string;
	oauthConfigId: string;
	mcpClientId: string;
}

export const OAuth2Authorizer: React.FC<OAuth2AuthorizerProps> = ({
	open,
	onClose,
	onSuccess,
	onError,
	authorizeUrl,
	oauthConfigId,
	mcpClientId,
}) => {
	const [status, setStatus] = useState<"pending" | "polling" | "success" | "failed">("pending");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	const popupRef = useRef<Window | null>(null);
	const pollIntervalRef = useRef<NodeJS.Timeout | null>(null);

	// Open popup and start polling
	const openPopup = () => {
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
	};

	// Poll OAuth status
	const startPolling = () => {
		// Clear any existing interval
		if (pollIntervalRef.current) {
			clearInterval(pollIntervalRef.current);
		}

		pollIntervalRef.current = setInterval(async () => {
			try {
				// Check if popup is still open
				if (popupRef.current && popupRef.current.closed) {
					stopPolling();
					setStatus("failed");
					setErrorMessage("Authorization cancelled");
					onError("Authorization cancelled");
					return;
				}

				// Poll OAuth status from API
				const response = await fetch(`/api/oauth/config/${oauthConfigId}/status`);
				const data = await response.json();

				if (data.status === "authorized") {
					// OAuth succeeded, now complete MCP client setup
					stopPolling();
					if (popupRef.current && !popupRef.current.closed) {
						popupRef.current.close();
					}

					// Call complete-oauth endpoint to add MCP client to Bifrost
					const completeResponse = await fetch(`/api/mcp/client/${mcpClientId}/complete-oauth`, {
						method: "POST",
					});

					if (completeResponse.ok) {
						setStatus("success");
						onSuccess();
						setTimeout(() => {
							onClose();
						}, 1000);
					} else {
						// Try to parse error response, fallback to status text if JSON parsing fails
						let errorMessage = "Failed to complete MCP client setup";
						try {
							const contentType = completeResponse.headers.get("content-type");
							if (contentType && contentType.includes("application/json")) {
								const errorData = await completeResponse.json();
								errorMessage = errorData.error?.message || errorData.error || errorData.message || errorMessage;
							} else {
								// Response is not JSON (likely HTML error page)
								const textResponse = await completeResponse.text();
								console.error("Received non-JSON response:", textResponse.substring(0, 200));
								errorMessage = `${errorMessage} (${completeResponse.status}: ${completeResponse.statusText})`;
							}
						} catch (parseError) {
							console.error("Error parsing response:", parseError);
							errorMessage = `${errorMessage} (${completeResponse.status}: ${completeResponse.statusText})`;
						}
						setStatus("failed");
						setErrorMessage(errorMessage);
						onError(errorMessage);
					}
				} else if (data.status === "failed" || data.status === "expired") {
					stopPolling();
					if (popupRef.current && !popupRef.current.closed) {
						popupRef.current.close();
					}
					setStatus("failed");
					setErrorMessage(`Authorization ${data.status}`);
					onError(`Authorization ${data.status}`);
				}
			} catch (error) {
				console.error("Error polling OAuth status:", error);
			}
		}, 2000); // Poll every 2 seconds
	};

	// Stop polling
	const stopPolling = () => {
		if (pollIntervalRef.current) {
			clearInterval(pollIntervalRef.current);
			pollIntervalRef.current = null;
		}
	};

	// Listen for postMessage from OAuth callback popup
	useEffect(() => {
		const handleMessage = (event: MessageEvent) => {
			// Verify message is from OAuth callback
			if (event.data?.type === "oauth_success") {
				// OAuth succeeded, stop polling and check status immediately
				stopPolling();
				// Trigger immediate status check
				checkOAuthStatus();
			}
		};

		window.addEventListener("message", handleMessage);
		return () => {
			window.removeEventListener("message", handleMessage);
		};
	}, [oauthConfigId, mcpClientId]);

	// Check OAuth status immediately (called by postMessage)
	const checkOAuthStatus = async () => {
		try {
			const response = await fetch(`/api/oauth/config/${oauthConfigId}/status`);
			const data = await response.json();

			if (data.status === "authorized") {
				// Close popup if still open
				if (popupRef.current && !popupRef.current.closed) {
					popupRef.current.close();
				}

				// Call complete-oauth endpoint
				const completeResponse = await fetch(`/api/mcp/client/${mcpClientId}/complete-oauth`, {
					method: "POST",
				});

				if (completeResponse.ok) {
					setStatus("success");
					onSuccess();
					setTimeout(() => {
						onClose();
					}, 1000);
				} else {
					// Try to parse error response, fallback to status text if JSON parsing fails
					let errorMessage = "Failed to complete MCP client setup";
					try {
						const contentType = completeResponse.headers.get("content-type");
						if (contentType && contentType.includes("application/json")) {
							const errorData = await completeResponse.json();
							errorMessage = errorData.error?.message || errorData.error || errorData.message || errorMessage;
						} else {
							// Response is not JSON (likely HTML error page)
							const textResponse = await completeResponse.text();
							console.error("Received non-JSON response:", textResponse.substring(0, 200));
							errorMessage = `${errorMessage} (${completeResponse.status}: ${completeResponse.statusText})`;
						}
					} catch (parseError) {
						console.error("Error parsing response:", parseError);
						errorMessage = `${errorMessage} (${completeResponse.status}: ${completeResponse.statusText})`;
					}
					setStatus("failed");
					setErrorMessage(errorMessage);
					onError(errorMessage);
				}
			}
		} catch (error) {
			console.error("Error checking OAuth status:", error);
		}
	};

	// Open popup when dialog opens
	useEffect(() => {
		if (open && status === "pending") {
			openPopup();
		}
	}, [open]);

	// Cleanup on unmount
	useEffect(() => {
		return () => {
			stopPolling();
			if (popupRef.current && !popupRef.current.closed) {
				popupRef.current.close();
			}
		};
	}, []);

	const handleRetry = () => {
		setStatus("pending");
		setErrorMessage(null);
		openPopup();
	};

	const handleCancel = () => {
		stopPolling();
		if (popupRef.current && !popupRef.current.closed) {
			popupRef.current.close();
		}
		onClose();
	};

	return (
		<Dialog open={open}>
			<DialogContent className="sm:max-w-md" onPointerDownOutside={(e) => e.preventDefault()} onEscapeKeyDown={(e) => e.preventDefault()}>
				<DialogHeader>
					<DialogTitle>OAuth Authorization</DialogTitle>
					<DialogDescription>
						{status === "pending" && "Opening authorization window..."}
						{status === "polling" && "Waiting for authorization..."}
						{status === "success" && "Authorization successful!"}
						{status === "failed" && "Authorization failed"}
					</DialogDescription>
				</DialogHeader>

				<div className="flex flex-col items-center justify-center space-y-4 py-4">
					{status === "polling" && (
						<>
							<Loader2 className="h-8 w-8 animate-spin text-blue-500" />
							<p className="text-muted-foreground text-sm">Please complete authorization in the popup window</p>
						</>
					)}

					{status === "success" && (
						<>
							<div className="flex h-12 w-12 items-center justify-center rounded-full bg-green-100">
								<svg className="h-6 w-6 text-green-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" />
								</svg>
							</div>
							<p className="text-sm text-green-600">MCP server connected successfully!</p>
						</>
					)}

					{status === "failed" && (
						<>
							<div className="flex h-12 w-12 items-center justify-center rounded-full bg-red-100">
								<svg className="h-6 w-6 text-red-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
								</svg>
							</div>
							<p className="text-sm text-red-600">{errorMessage || "An error occurred"}</p>
							<Button onClick={handleRetry} variant="outline">
								Retry
							</Button>
						</>
					)}
				</div>

				{status === "polling" && (
					<div className="flex justify-end space-x-2">
						<Button onClick={handleCancel} variant="outline">
							Cancel
						</Button>
					</div>
				)}
			</DialogContent>
		</Dialog>
	);
};
