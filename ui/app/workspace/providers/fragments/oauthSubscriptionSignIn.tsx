import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage } from "@/lib/store";
import {
	type AntigravityOAuthStartResponse,
	type KiroOAuthMethod,
	type KiroOAuthStartResponse,
	useCompleteAntigravityOAuthMutation,
	usePollKiroOAuthMutation,
	useStartAntigravityOAuthMutation,
	useStartKiroOAuthMutation,
} from "@/lib/store/apis/oauthSubscriptionsApi";
import { Check, Copy, ExternalLink, Loader2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

// Called once a sign-in flow produces a credential: the string to store as the key's value,
// plus a key name the server suggests (usually the account email).
export type OnOAuthCredential = (credential: string, suggestedName?: string) => void;

interface SignInProps {
	onCredential: OnOAuthCredential;
	disabled?: boolean;
}

export function AntigravitySignIn({ onCredential, disabled }: SignInProps) {
	const [startOAuth, { isLoading: isStarting }] = useStartAntigravityOAuthMutation();
	const [completeOAuth, { isLoading: isCompleting }] = useCompleteAntigravityOAuthMutation();
	const [session, setSession] = useState<AntigravityOAuthStartResponse | null>(null);
	const [callbackUrl, setCallbackUrl] = useState("");

	const start = async () => {
		try {
			const res = await startOAuth().unwrap();
			setSession(res);
			setCallbackUrl("");
			window.open(res.auth_url, "_blank", "noopener,noreferrer");
		} catch (err) {
			toast.error("Failed to start Google sign-in", { description: getErrorMessage(err) });
		}
	};

	const complete = async () => {
		if (!session || !callbackUrl.trim()) return;
		try {
			const res = await completeOAuth({ session_id: session.session_id, callback_url: callbackUrl.trim() }).unwrap();
			onCredential(res.credential, res.suggested_name);
			setSession(null);
			setCallbackUrl("");
			toast.success(res.email ? `Signed in as ${res.email}` : "Signed in with Google", {
				description: "The credential was filled in. Save the key to store it.",
			});
		} catch (err) {
			toast.error("Failed to complete Google sign-in", { description: getErrorMessage(err) });
		}
	};

	return (
		<div className="space-y-3 rounded-md border p-3" data-testid="oauth-signin-panel-antigravity">
			<div className="flex items-center justify-between gap-2">
				<p className="text-muted-foreground text-xs">Sign in with the Google account that holds the Antigravity subscription.</p>
				<Button
					type="button"
					variant="outline"
					size="sm"
					onClick={start}
					disabled={disabled || isStarting || isCompleting}
					isLoading={isStarting}
					data-testid="oauth-signin-btn-antigravity"
				>
					{session ? "Restart sign-in" : "Sign in with Google"}
				</Button>
			</div>
			{session && (
				<div className="space-y-3">
					<Alert variant="info">
						<AlertDescription className="block space-y-1 text-xs">
							<p>
								A Google consent page opened in a new tab (
								<a
									href={session.auth_url}
									target="_blank"
									rel="noopener noreferrer"
									className="underline"
									data-testid="oauth-signin-link-antigravity"
								>
									open it again
								</a>
								).
							</p>
							<p>
								After you consent, the browser is redirected to a <code>127.0.0.1:51121</code> page that will probably fail to load. That
								is expected: copy the full URL from the address bar and paste it below.
							</p>
						</AlertDescription>
					</Alert>
					<div className="space-y-1.5">
						<Label htmlFor="oauth-callback-url-antigravity">Redirected URL</Label>
						<div className="flex gap-2">
							<Input
								id="oauth-callback-url-antigravity"
								placeholder="http://127.0.0.1:51121/callback?state=...&code=..."
								value={callbackUrl}
								onChange={(e) => setCallbackUrl(e.target.value)}
								onKeyDown={(e) => {
									// Enter would otherwise submit the surrounding key form.
									if (e.key === "Enter") {
										e.preventDefault();
										void complete();
									}
								}}
								disabled={disabled || isCompleting}
								data-testid="oauth-callback-input-antigravity"
							/>
							<Button
								type="button"
								size="sm"
								onClick={complete}
								disabled={disabled || !callbackUrl.trim() || isCompleting}
								isLoading={isCompleting}
								data-testid="oauth-complete-btn-antigravity"
							>
								Complete
							</Button>
						</div>
					</div>
				</div>
			)}
		</div>
	);
}

const KIRO_METHODS: { value: KiroOAuthMethod; label: string }[] = [
	{ value: "builder-id", label: "AWS Builder ID" },
	{ value: "google", label: "Google" },
	{ value: "github", label: "GitHub" },
];

type KiroFlowStatus = "idle" | "waiting" | "expired" | "error";

export function KiroSignIn({ onCredential, disabled }: SignInProps) {
	const [startOAuth, { isLoading: isStarting }] = useStartKiroOAuthMutation();
	const [pollOAuth] = usePollKiroOAuthMutation();
	const [method, setMethod] = useState<KiroOAuthMethod>("builder-id");
	const [session, setSession] = useState<KiroOAuthStartResponse | null>(null);
	const [status, setStatus] = useState<KiroFlowStatus>("idle");
	const [errorMessage, setErrorMessage] = useState<string | null>(null);
	const { copy, copied } = useCopyToClipboard({ successMessage: "Code copied" });

	const start = async () => {
		try {
			const res = await startOAuth({ method }).unwrap();
			setErrorMessage(null);
			setSession(res);
			setStatus("waiting");
		} catch (err) {
			toast.error("Failed to start Kiro sign-in", { description: getErrorMessage(err) });
		}
	};

	const cancel = () => {
		setSession(null);
		setStatus("idle");
		setErrorMessage(null);
	};

	// Read through a ref so a parent re-render (new callback identity) does not reset the poll timer.
	const onCredentialRef = useRef(onCredential);
	useEffect(() => {
		onCredentialRef.current = onCredential;
	}, [onCredential]);

	// Poll until the device authorization resolves. The effect owns the timer chain, so changing
	// the session (restart/cancel) or unmounting the form stops it.
	useEffect(() => {
		if (!session || status !== "waiting") return;
		let cancelled = false;
		let timer: number | undefined;
		const expiresAt = Date.parse(session.expires_at);

		const schedule = (seconds: number) => {
			timer = window.setTimeout(poll, Math.max(1, seconds) * 1000);
		};

		const poll = async () => {
			if (cancelled) return;
			if (!Number.isNaN(expiresAt) && Date.now() >= expiresAt) {
				setStatus("expired");
				return;
			}
			try {
				const res = await pollOAuth({ session_id: session.session_id }).unwrap();
				if (cancelled) return;
				switch (res.status) {
					case "pending":
						schedule(res.interval_seconds || session.interval_seconds);
						return;
					case "complete":
						if (!res.credential) {
							setErrorMessage("Sign-in completed but the server returned no credential.");
							setStatus("error");
							return;
						}
						onCredentialRef.current(res.credential, res.suggested_name);
						setSession(null);
						setStatus("idle");
						toast.success("Signed in to Kiro", { description: "The credential was filled in. Save the key to store it." });
						return;
					case "expired":
						setStatus("expired");
						return;
					default:
						setErrorMessage(res.error || "Sign-in failed.");
						setStatus("error");
				}
			} catch (err) {
				if (cancelled) return;
				setErrorMessage(getErrorMessage(err));
				setStatus("error");
			}
		};

		schedule(session.interval_seconds);
		return () => {
			cancelled = true;
			window.clearTimeout(timer);
		};
	}, [session, status, pollOAuth]);

	const verificationUrl = session ? session.verification_uri_complete || session.verification_uri : "";

	return (
		<div className="space-y-3 rounded-md border p-3" data-testid="oauth-signin-panel-kiro">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<p className="text-muted-foreground text-xs">Sign in to the Kiro account that holds the subscription.</p>
				<div className="flex items-center gap-2">
					<Select value={method} onValueChange={(v) => setMethod(v as KiroOAuthMethod)} disabled={disabled || status === "waiting"}>
						<SelectTrigger className="h-8 w-40" data-testid="oauth-method-select-kiro">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{KIRO_METHODS.map((m) => (
								<SelectItem key={m.value} value={m.value} data-testid={`oauth-method-option-${m.value}`}>
									{m.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={start}
						disabled={disabled || isStarting}
						isLoading={isStarting}
						data-testid="oauth-signin-btn-kiro"
					>
						{session ? "Restart sign-in" : "Sign in"}
					</Button>
				</div>
			</div>
			{session && (
				<div className="space-y-3" data-testid="oauth-device-panel-kiro">
					<div className="flex flex-col items-center gap-2 rounded-md border border-dashed p-4">
						<span className="text-muted-foreground text-xs">Enter this code on the sign-in page</span>
						<div className="flex items-center gap-2">
							<span className="font-mono text-2xl font-semibold tracking-[0.3em]" data-testid="oauth-user-code-kiro">
								{session.user_code}
							</span>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								onClick={() => copy(session.user_code)}
								aria-label="Copy code"
								data-testid="oauth-user-code-copy-btn-kiro"
							>
								{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
							</Button>
						</div>
						<a
							href={verificationUrl}
							target="_blank"
							rel="noopener noreferrer"
							className="text-primary inline-flex items-center gap-1 text-sm underline"
							data-testid="oauth-verification-link-kiro"
						>
							Open sign-in page
							<ExternalLink className="h-3.5 w-3.5" />
						</a>
					</div>
					{status === "waiting" && (
						<div className="text-muted-foreground flex items-center justify-between gap-2 text-xs" data-testid="oauth-status-kiro">
							<span className="inline-flex items-center gap-2">
								<Loader2 className="h-3.5 w-3.5 animate-spin" />
								Waiting for you to approve the sign-in...
							</span>
							<Button type="button" variant="ghost" size="sm" onClick={cancel} data-testid="oauth-cancel-btn-kiro">
								Cancel
							</Button>
						</div>
					)}
					{status === "expired" && (
						<Alert variant="warning">
							<AlertDescription className="text-xs" data-testid="oauth-status-kiro">
								The code expired before sign-in finished. Click &quot;Restart sign-in&quot; to get a new one.
							</AlertDescription>
						</Alert>
					)}
					{status === "error" && (
						<Alert variant="destructive">
							<AlertDescription className="text-xs" data-testid="oauth-status-kiro">
								{errorMessage ?? "Sign-in failed."} Click &quot;Restart sign-in&quot; to try again.
							</AlertDescription>
						</Alert>
					)}
				</div>
			)}
		</div>
	);
}
