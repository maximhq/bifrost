import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { toast } from "sonner";
import {
	getErrorMessage,
	useGetOAuth2ConsentFlowQuery,
	useIsAuthEnabledQuery,
	useSubmitOAuth2ConsentFlowMutation,
} from "@/lib/store";
import {
	getActiveTempToken,
	setActiveTempToken,
	setSuppressGlobal401,
} from "@/lib/store/apis/tempToken";
import {
	Fingerprint,
	KeyRound,
	Loader2,
	LogIn,
	ShieldCheck,
	UserRound,
} from "lucide-react";
import { useQueryState } from "nuqs";
import React, { useEffect, useMemo, useState } from "react";

export default function OAuth2ConsentPage() {
	const [flowId] = useQueryState("flow");

	if (!flowId) {
		return (
			<Shell>
				<div className="text-center">
					<h1 className="text-xl font-semibold">Missing flow identifier</h1>
					<p className="text-muted-foreground mt-2 text-sm">
						This URL is missing the{" "}
						<code className="bg-muted rounded px-1 py-0.5 text-xs">flow</code>{" "}
						query parameter. Restart the connection from your MCP client.
					</p>
				</div>
			</Shell>
		);
	}

	return <ConsentView flowId={flowId} />;
}

// isSafeRedirect rejects URLs whose scheme could execute script when assigned to
// location.href (javascript:, data:, …) while allowing http(s) and any native
// custom-scheme redirect a client may have registered.
function isSafeRedirect(url: string): boolean {
	try {
		const proto = new URL(url, window.location.origin).protocol.toLowerCase();
		return !["javascript:", "data:", "vbscript:", "blob:", "file:"].includes(proto);
	} catch {
		return false;
	}
}

function ConsentView({ flowId }: { flowId: string }) {
	const { data: flow, isLoading, isError, error } = useGetOAuth2ConsentFlowQuery(flowId);
	const { data: authState } = useIsAuthEnabledQuery();
	const [submitFlow, { isLoading: submitting }] = useSubmitOAuth2ConsentFlowMutation();
	const [vkValue, setVkValue] = useState("");
	const [selectedMode, setSelectedMode] = useState<"vk" | "session" | "user" | null>(null);

	// Restore a temp token persisted across a login round-trip BEFORE sampling
	// usingTempToken, so returning to consent after cancelling login (where the
	// module-level token was already cleared) still surfaces the sign-in path.
	const [usingTempToken] = useState(() => {
		if (typeof sessionStorage !== "undefined") {
			const stored = sessionStorage.getItem(`oauth2_consent_token_${flowId}`);
			if (stored && !getActiveTempToken()) {
				setActiveTempToken(stored);
				setSuppressGlobal401(true);
				sessionStorage.removeItem(`oauth2_consent_token_${flowId}`);
			}
		}
		return getActiveTempToken() !== null;
	});

	const loginHref = useMemo(() => {
		const returnPath = `/oauth/consent?flow=${encodeURIComponent(flowId)}`;
		return `/login?goto=${encodeURIComponent(returnPath)}`;
	}, [flowId]);

	// Persist the active temp token so it can be restored after a login round-trip.
	// Kept in an effect (not the memo above) so it runs exactly once per flowId —
	// memo computations are not guaranteed to run in React 18 concurrent mode.
	useEffect(() => {
		if (typeof sessionStorage === "undefined") return;
		const currentToken = getActiveTempToken();
		if (currentToken) {
			sessionStorage.setItem(`oauth2_consent_token_${flowId}`, currentToken);
		}
	}, [flowId]);

	const showLoginOption =
		usingTempToken &&
		authState?.is_auth_enabled === true &&
		authState.has_valid_token === false;

	const handleSubmit = async (mode: "vk" | "session" | "user") => {
		setSelectedMode(mode);
		try {
			const res = await submitFlow({
				flowId,
				body: { mode, value: mode === "vk" ? vkValue : undefined },
			}).unwrap();
			// Defence-in-depth: the server validates redirect_uri against the
			// registered client, but never hand a javascript:/data: URL to
			// location.href — it would execute in this origin. Block dangerous
			// schemes while still allowing http(s) and native custom-scheme
			// redirects that clients may register.
			if (!isSafeRedirect(res.redirect_url)) {
				toast.error("Authentication failed", { description: "Invalid redirect URL" });
				setSelectedMode(null);
				return;
			}
			window.location.href = res.redirect_url;
		} catch (err) {
			toast.error("Authentication failed", { description: getErrorMessage(err) });
			setSelectedMode(null);
		}
	};

	if (isLoading) return <FullPageLoader />;

	if (isError || !flow) {
		const status = (error as { status?: number } | undefined)?.status;
		if (status === 401) return <InvalidLinkView />;
		return (
			<Shell>
				<div className="text-center">
					<h1 className="text-xl font-semibold">Link unavailable</h1>
					<p className="text-muted-foreground mt-2 text-sm">
						This authorization link may have expired or already been used.
						Restart the connection from your MCP client to get a fresh link.
					</p>
				</div>
			</Shell>
		);
	}

	const hasUser = flow.available_modes.includes("user");
	const hasVK = flow.available_modes.includes("vk");
	const hasSession = flow.available_modes.includes("session");
	const hasAnyMode = hasUser || hasVK || hasSession;
	const clientName = flow.client_name || "MCP Client";

	return (
		<Shell>
			{/* Header */}
			<div className="mb-8 text-center">
				<div className="bg-primary/10 mx-auto mb-4 flex size-14 items-center justify-center rounded-full">
					<ShieldCheck className="text-primary size-7" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">
					{clientName} wants to connect
				</h1>
				<p className="text-muted-foreground mt-1.5 text-sm">
					Choose how you'd like to identify yourself to Bifrost
				</p>
			</div>

			<div className="space-y-3">
				{/* No mode available — nothing the user can act on here */}
				{!hasAnyMode && (
					<div
						className="rounded-sm border p-4 text-center"
						data-testid="oauth-consent-empty-state"
					>
						<p className="text-sm font-medium">
							No authentication options available
						</p>
						<p className="text-muted-foreground mt-1 text-xs">
							Restart the connection from your MCP client.
						</p>
					</div>
				)}

				{/* User mode — most prominent when logged in */}
				{hasUser && flow.logged_in_user && (
					<div className="rounded-sm border p-4">
						<div className="flex items-center gap-3">
							<div className="bg-muted flex size-9 shrink-0 items-center justify-center rounded-full">
								<UserRound className="text-muted-foreground size-4" />
							</div>
							<div className="min-w-0 flex-1">
								<p className="text-sm font-medium leading-tight">
									{flow.logged_in_user.name || flow.logged_in_user.id}
								</p>
								<p className="text-muted-foreground text-xs">Signed-in account</p>
							</div>
						</div>
						<Button
							data-testid="oauth-consent-continue-user-btn"
							className="mt-4 w-full"
							onClick={() => handleSubmit("user")}
							disabled={submitting}
						>
							{submitting && selectedMode === "user" ? (
								<><Loader2 className="mr-2 size-4 animate-spin" />Connecting…</>
							) : (
								<>Continue as {flow.logged_in_user.name || flow.logged_in_user.id}</>
							)}
						</Button>
					</div>
				)}

				{/* User mode — sign in prompt when not logged in */}
				{hasUser && !flow.logged_in_user && showLoginOption && (
					<div className="rounded-sm border p-4">
						<div className="flex items-center gap-3">
							<div className="bg-muted flex size-9 shrink-0 items-center justify-center rounded-full">
								<UserRound className="text-muted-foreground size-4" />
							</div>
							<div>
								<p className="text-sm font-medium">Sign in with your account</p>
								<p className="text-muted-foreground text-xs">
									Requires a Bifrost dashboard account
								</p>
							</div>
						</div>
						<Button asChild variant="outline" className="mt-4 w-full">
							<a href={loginHref} data-testid="oauth-consent-signin-link">
								<LogIn className="mr-2 size-4" />
								Sign in to continue
							</a>
						</Button>
					</div>
				)}

				{/* Divider between user and key options */}
				{hasUser && (hasVK || hasSession) && (
					<div className="relative">
						<Separator />
						<span className="bg-card text-muted-foreground absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 px-2 text-xs">
							or
						</span>
					</div>
				)}

				{/* VK mode */}
				{hasVK && (
					<div className="rounded-sm border p-4">
						<div className="mb-4 flex items-center gap-3">
							<div className="bg-muted flex size-9 shrink-0 items-center justify-center rounded-full">
								<KeyRound className="text-muted-foreground size-4" />
							</div>
							<div>
								<p className="text-sm font-medium">Virtual Key</p>
								<p className="text-muted-foreground text-xs">
									Use a Virtual Key from your Bifrost workspace
								</p>
							</div>
						</div>
						<Input
							id="vk-input"
							data-testid="oauth-consent-vk-input"
							type="password"
							placeholder="sk-bf-…"
							value={vkValue}
							onChange={(e) => setVkValue(e.target.value)}
							onKeyDown={(e) => {
								if (e.key === "Enter" && vkValue.trim()) {
									void handleSubmit("vk");
								}
							}}
							disabled={submitting}
							className="mb-3"
						/>
						<Button
							data-testid="oauth-consent-connect-vk-btn"
							className="w-full"
							onClick={() => handleSubmit("vk")}
							disabled={submitting || !vkValue.trim()}
						>
							{submitting && selectedMode === "vk" ? (
								<><Loader2 className="mr-2 size-4 animate-spin" />Connecting…</>
							) : (
								"Connect with key"
							)}
						</Button>
						{hasUser && (
							<p className="text-muted-foreground mt-2.5 text-xs">
								If this key is linked to a user account, you'll be asked to sign
								in to confirm your identity.
							</p>
						)}
					</div>
				)}

				{hasVK && hasSession && (
					<div className="relative">
						<Separator />
						<span className="bg-card text-muted-foreground absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 px-2 text-xs">
							or
						</span>
					</div>
				)}

				{/* Session mode — de-emphasised, last */}
				{hasSession && (
					<Button
						data-testid="oauth-consent-continue-session-btn"
						variant="ghost"
						className="text-muted-foreground hover:text-foreground w-full justify-start gap-3 px-4 py-3 h-auto"
						onClick={() => handleSubmit("session")}
						disabled={submitting}
					>
						<Fingerprint className="size-4 shrink-0" />
						<div className="text-left">
							<span className="block text-sm font-normal">
								{submitting && selectedMode === "session"
									? "Connecting…"
									: "Continue without an identity"}
							</span>
							<span className="text-xs opacity-70">
								Anonymous session - no account required
							</span>
						</div>
					</Button>
				)}
			</div>

			{/* Expiry */}
			<p className="text-muted-foreground mt-6 text-center text-xs">
				This link expires {formatExpiry(flow.expires_at)}
			</p>
		</Shell>
	);
}

function formatExpiry(iso: string): string {
	const ts = new Date(iso).getTime();
	if (Number.isNaN(ts)) return "soon";
	try {
		const diffMs = ts - Date.now();
		if (diffMs < 0) return "soon";
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 1) return "in less than a minute";
		if (mins === 1) return "in 1 minute";
		return `in ${mins} minutes`;
	} catch {
		return "soon";
	}
}

function Shell({ children }: { children: React.ReactNode }) {
	return (
		<div className="mx-auto flex min-h-screen w-full items-center justify-center p-4 sm:p-6">
			<div className="bg-card w-full max-w-md rounded-sm border p-6 shadow-sm sm:p-8">
				{children}
			</div>
		</div>
	);
}

function InvalidLinkView() {
	return (
		<Shell>
			<div className="text-center">
				<h1 className="text-xl font-semibold tracking-tight">
					This link is no longer valid
				</h1>
				<p className="text-muted-foreground mt-2 text-sm">
					The authorization link has expired, been used already, or had its
					token stripped. Restart the connection from your MCP client to get a
					fresh link.
				</p>
			</div>
		</Shell>
	);
}
