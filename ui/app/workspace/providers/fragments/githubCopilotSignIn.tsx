import { Button } from "@/components/ui/button";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage, usePollGithubCopilotSignInMutation, useStartGithubCopilotSignInMutation } from "@/lib/store";
import type { GithubCopilotDeviceCode } from "@/lib/types/config";
import { Check, Copy, ExternalLink, Github, Loader2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";

interface Props {
	provider: string;
	// onToken receives the signed-in GitHub token; the key form saves it as the key's value.
	onToken: (token: string) => void;
}

// GithubCopilotSignIn runs "Sign in with GitHub" for a Copilot key. The gateway calls GitHub's
// device flow (the browser cannot: GitHub sends no CORS headers); this shows the code to enter
// on github.com, polls until the operator approves, and hands the token to the form.
export function GithubCopilotSignIn({ provider, onToken }: Props) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const [startSignIn, { isLoading: isStarting }] = useStartGithubCopilotSignInMutation();
	const [pollSignIn] = usePollGithubCopilotSignInMutation();
	const [code, setCode] = useState<GithubCopilotDeviceCode | null>(null);
	const [login, setLogin] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	const { copy, copied } = useCopyToClipboard();
	const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
	const generation = useRef(0);

	const stopPolling = () => {
		generation.current++;
		if (timer.current) clearTimeout(timer.current);
		timer.current = null;
	};
	useEffect(() => stopPolling, []);

	const poll = (deviceCode: string, intervalSeconds: number) => {
		const gen = generation.current;
		timer.current = setTimeout(async () => {
			try {
				const result = await pollSignIn({ provider, deviceCode }).unwrap();
				if (gen !== generation.current) return;
				if (result.status === "pending") {
					poll(deviceCode, result.interval || intervalSeconds);
					return;
				}
				setCode(null);
				if (result.status === "complete" && result.token) {
					onToken(result.token);
					setLogin(result.login || "");
				} else {
					setError(result.status === "expired" ? t("providers.githubCopilot.codeExpired") : t("providers.githubCopilot.signInDenied"));
				}
			} catch (err) {
				if (gen !== generation.current) return;
				setCode(null);
				setError(getErrorMessage(err));
			}
		}, intervalSeconds * 1000);
	};

	const start = async () => {
		stopPolling();
		const gen = generation.current;
		setError(null);
		setLogin(null);
		try {
			const deviceCode = await startSignIn({ provider }).unwrap();
			if (gen !== generation.current) return;
			setCode(deviceCode);
			poll(deviceCode.device_code, deviceCode.interval || 5);
		} catch (err) {
			if (gen !== generation.current) return;
			setError(getErrorMessage(err));
		}
	};

	const cancel = () => {
		stopPolling();
		setCode(null);
	};

	if (code) {
		return (
			<div className="space-y-3 rounded-sm border p-3 text-sm" data-testid="copilot-sign-in-steps">
				<ol className="space-y-3">
					<li className="flex items-center gap-2">
						<StepNumber n={1} />
						<span>
							<Trans
								t={t}
								i18nKey="providers.githubCopilot.openLink"
								components={{
									link: (
										<a
											href={code.verification_uri}
											target="_blank"
											rel="noopener noreferrer"
											className="text-primary inline-flex items-center gap-1 hover:underline"
											data-testid="copilot-sign-in-verification-link"
										>
											{code.verification_uri} <ExternalLink className="h-3 w-3" />
										</a>
									),
								}}
							/>
						</span>
					</li>
					<li className="flex items-center gap-2">
						<StepNumber n={2} />
						<span>{t("providers.githubCopilot.enter")}</span>
						<code data-testid="copilot-sign-in-user-code" className="bg-muted rounded-sm px-2 py-0.5 font-mono text-base">
							{code.user_code}
						</code>
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => copy(code.user_code)}
							aria-label={t("providers.githubCopilot.copyCode")}
							data-testid="copilot-sign-in-copy-code"
						>
							{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
						</Button>
						<span>{t("providers.githubCopilot.approveAccess")}</span>
					</li>
					<li className="flex items-center gap-2">
						<StepNumber n={3} />
						<span className="text-muted-foreground inline-flex items-center gap-1">
							<Loader2 className="h-3 w-3 animate-spin" /> {t("providers.githubCopilot.waitingForApproval")}
						</span>
						<Button type="button" variant="ghost" size="sm" onClick={cancel} data-testid="copilot-sign-in-cancel">
							{tc("cancel")}
						</Button>
					</li>
				</ol>
			</div>
		);
	}

	return (
		<div className="space-y-2">
			<Button type="button" variant="outline" onClick={start} disabled={isStarting} data-testid="copilot-sign-in-button">
				{isStarting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Github className="h-4 w-4" />}
				{t("providers.githubCopilot.signIn")}
			</Button>
			{login !== null && (
				<p className="text-sm" data-testid="copilot-sign-in-status">
					{login ? t("providers.githubCopilot.signedInAs", { login }) : t("providers.githubCopilot.signedIn")}
				</p>
			)}
			{error && (
				<p className="text-destructive text-sm" data-testid="copilot-sign-in-error">
					{error}
				</p>
			)}
		</div>
	);
}

// StepNumber is the numbered marker of one device-flow step.
function StepNumber({ n }: { n: number }) {
	return (
		<span className="bg-muted text-muted-foreground flex size-5 shrink-0 items-center justify-center rounded-full text-xs font-medium">
			{n}
		</span>
	);
}