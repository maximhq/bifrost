"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { getErrorMessage, useLazyGetProviderQuery } from "@/lib/store";
import {
	useCancelCodexAuthSessionMutation,
	useLazyGetCodexAuthSessionQuery,
	useStartCodexDeviceAuthMutation,
} from "@/lib/store/apis/providersApi";
import { EnvVar } from "@/lib/types/schemas";
import { ExternalLink, Loader2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { UseFormReturn, useWatch } from "react-hook-form";

type CodexAuthSession = {
	id: string;
	flow_type: string;
	status: string;
	expires_at: string;
	verification_uri?: string;
	user_code?: string;
	interval_seconds?: number;
	next_poll_at?: string;
	last_error?: string;
	completed_at?: string;
};

interface CodexAuthControlsProps {
	providerName: string;
	keyId: string;
	form: UseFormReturn<any>;
	isEditing: boolean;
	isConfigManaged: boolean;
	onEnsurePersisted?: (authMethod?: "device" | "manual") => Promise<string | null>;
}

export function CodexAuthControls({ providerName, keyId, form, isEditing, isConfigManaged, onEnsurePersisted }: CodexAuthControlsProps) {
	const [startDeviceAuth, { isLoading: isStartingDevice }] = useStartCodexDeviceAuthMutation();
	const [cancelCodexAuthSession] = useCancelCodexAuthSessionMutation();
	const [getCodexAuthSession] = useLazyGetCodexAuthSessionQuery();
	const [getProvider] = useLazyGetProviderQuery();
	const [session, setSession] = useState<CodexAuthSession | null>(null);
	const [isOpen, setIsOpen] = useState(false);
	const [statusMessage, setStatusMessage] = useState<string | null>(null);
	const popupRef = useRef<Window | null>(null);
	const watchedRefreshToken = useWatch({ control: form.control, name: "key.codex_key_config.refresh_token" }) as EnvVar | undefined;
	const watchedAccessToken = useWatch({ control: form.control, name: "key.codex_key_config.access_token" }) as EnvVar | undefined;
	const watchedAccountID = useWatch({ control: form.control, name: "key.codex_key_config.account_id" }) as EnvVar | undefined;

	const isConnected = useMemo(() => {
		return Boolean(
			watchedRefreshToken?.value ||
				watchedRefreshToken?.env_var ||
				watchedAccessToken?.value ||
				watchedAccessToken?.env_var ||
				watchedAccountID?.value ||
				watchedAccountID?.env_var,
		);
	}, [watchedAccessToken, watchedAccountID, watchedRefreshToken]);

	const keyStatus = useMemo(() => {
		if (isConfigManaged) {
			return {
				label: "Config managed",
				description: "This key is managed from config.json, so the UI cannot change its authentication state.",
				variant: "secondary" as const,
			};
		}
		if (session?.status === "pending") {
			return {
				label: "Waiting for authorization",
				description: "The sign-in flow has started. Complete the OpenAI verification step to finish connecting this key.",
				variant: "secondary" as const,
			};
		}
		if (session?.status === "authorized" || isConnected) {
			return {
				label: "Connected",
				description: "Bifrost has a stored Codex credential for this key and can use it for requests.",
				variant: "default" as const,
			};
		}
		if (session?.status === "failed" || session?.status === "expired" || session?.status === "cancelled") {
			return {
				label: "Not connected",
				description:
					session.last_error || `The last authorization attempt ended as ${session.status}. Start a new connection flow to continue.`,
				variant: "destructive" as const,
			};
		}
		return {
			label: "Not connected",
			description: "This key does not have a stored Codex credential yet.",
			variant: "outline" as const,
		};
	}, [isConfigManaged, isConnected, session]);

	const syncFormFromProvider = useCallback(async () => {
		const updatedProvider = await getProvider(providerName).unwrap();
		const updatedKey = updatedProvider.keys.find((key) => key.id === keyId);
		if (updatedKey?.codex_key_config) {
			form.setValue("key.codex_key_config", updatedKey.codex_key_config, { shouldDirty: false });
		}
	}, [form, getProvider, keyId, providerName]);

	useEffect(() => {
		if (!session || session.status !== "pending") {
			return;
		}
		const timer = window.setInterval(async () => {
			try {
				const nextSession = await getCodexAuthSession(session.id).unwrap();
				setSession(nextSession);
				if (nextSession.status === "authorized") {
					setStatusMessage("Authorization successful");
					await syncFormFromProvider();
				}
				if (nextSession.status === "failed" || nextSession.status === "expired" || nextSession.status === "cancelled") {
					setStatusMessage(nextSession.last_error || `Authorization ${nextSession.status}`);
				}
			} catch (error) {
				setStatusMessage(getErrorMessage(error));
			}
		}, 2000);
		return () => window.clearInterval(timer);
	}, [getCodexAuthSession, session, syncFormFromProvider]);

	const ensureKeyID = useCallback(
		async (authMethod: "device") => {
			form.setValue("key.codex_key_config.auth_method", authMethod, { shouldDirty: true });
			if (!isEditing && onEnsurePersisted) {
				const persistedKeyID = await onEnsurePersisted(authMethod);
				if (!persistedKeyID) {
					throw new Error("Key name is required before starting Codex authentication");
				}
				return persistedKeyID;
			}
			return keyId;
		},
		[form, isEditing, keyId, onEnsurePersisted],
	);

	const beginDeviceFlow = async () => {
		setStatusMessage(null);
		const resolvedKeyID = await ensureKeyID("device");
		const nextSession = await startDeviceAuth(resolvedKeyID).unwrap();
		setSession(nextSession);
		setIsOpen(true);
		if (nextSession.verification_uri) {
			popupRef.current = window.open(
				nextSession.verification_uri,
				"codex_device_auth",
				"width=640,height=760,resizable=yes,scrollbars=yes",
			);
		}
	};

	const handleCancel = async () => {
		if (session) {
			await cancelCodexAuthSession(session.id)
				.unwrap()
				.catch(() => undefined);
		}
		setIsOpen(false);
		setSession(null);
		setStatusMessage(null);
	};

	const handleCloseDialog = () => {
		if (popupRef.current && !popupRef.current.closed) {
			popupRef.current.close();
		}
		setIsOpen(false);
	};

	if (isConfigManaged) {
		return (
			<Alert>
				<AlertTitle>Interactive auth disabled</AlertTitle>
				<AlertDescription>
					Codex browser/device authentication is disabled for keys managed from `config.json`. Update the Codex credentials in config mode
					instead.
				</AlertDescription>
			</Alert>
		);
	}

	return (
		<div className="space-y-3 rounded-sm border p-4">
			<div className="flex items-start justify-between gap-3 rounded-sm border p-3">
				<div className="space-y-1">
					<div className="text-sm font-medium">Key Status</div>
					<p className="text-muted-foreground text-xs">{keyStatus.description}</p>
				</div>
				<Badge variant={keyStatus.variant}>{keyStatus.label}</Badge>
			</div>
			<div className="space-y-1">
				<div className="text-sm font-medium">Interactive Authentication</div>
				<p className="text-muted-foreground text-xs">
					Click connect, sign in with OpenAI, and enter the verification code shown here. Bifrost stores the resulting Codex credentials for
					you.
				</p>
			</div>
			{!isEditing ? (
				<Alert>
					<AlertTitle>Ready to connect</AlertTitle>
					<AlertDescription>
						Click connect below. If this is a new key, Bifrost will save the draft automatically before opening the sign-in link and code
						flow.
					</AlertDescription>
				</Alert>
			) : null}
			<div className="flex flex-wrap gap-2">
				<Button
					type="button"
					variant="outline"
					onClick={() => void beginDeviceFlow()}
					disabled={isStartingDevice}
					data-testid="codex-connect-btn"
				>
					{isStartingDevice ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
					{isConnected ? "Reconnect with OpenAI" : "Connect with OpenAI"}
				</Button>
			</div>
			{isConnected ? <p className="text-xs text-green-600">This key already has Codex credentials configured.</p> : null}

			<Dialog open={isOpen} onOpenChange={(open) => setIsOpen(open)}>
				<DialogContent>
					<DialogHeader>
						<DialogTitle>Connect ChatGPT Plus/Pro</DialogTitle>
						<DialogDescription>Open the verification link, sign in, then enter the code below on the OpenAI page.</DialogDescription>
					</DialogHeader>

					<div className="space-y-4 text-sm">
						{session?.verification_uri ? (
							<div className="space-y-2">
								<div className="font-medium">Step 1: Open sign-in link</div>
								<a
									href={session.verification_uri}
									target="_blank"
									rel="noreferrer"
									className="inline-flex items-center gap-2 text-blue-600 underline"
								>
									Open verification page
									<ExternalLink className="h-3 w-3" />
								</a>
							</div>
						) : null}

						{session?.user_code ? (
							<div className="space-y-2">
								<div className="font-medium">Step 2: Enter this code</div>
								<Input readOnly value={session.user_code} className="font-mono" data-testid="codex-device-user-code" />
							</div>
						) : null}

						<div className="text-muted-foreground rounded-sm border p-3 text-xs">
							Status: {session?.status ?? "pending"}
							{statusMessage ? <div className="text-foreground mt-2">{statusMessage}</div> : null}
						</div>
					</div>

					<DialogFooter>
						{session?.status === "pending" ? (
							<Button type="button" variant="outline" onClick={() => void handleCancel()}>
								Cancel authorization
							</Button>
						) : null}
						<Button type="button" variant="outline" onClick={handleCloseDialog}>
							Close
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}
