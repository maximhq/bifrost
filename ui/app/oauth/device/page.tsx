import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { getEndpointUrl } from "@/lib/utils/port";
import { CheckCircle2, Loader2, Terminal } from "lucide-react";
import { useQueryState } from "nuqs";
import React, { useState } from "react";
import { useTranslation } from "react-i18next";

// Same charset and shape the gateway issues (RFC 8628 §6.1 base-20 consonants).
const USER_CODE_LENGTH = 8;

function normalizeUserCode(value: string): string {
	return value.toUpperCase().replace(/[^A-Z]/g, "");
}

function formatUserCode(value: string): string {
	const code = normalizeUserCode(value).slice(0, USER_CODE_LENGTH);
	return code.length > 4 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
}

export default function ClaudeCodeDevicePage() {
	const { t } = useTranslation("login");
	const [approved] = useQueryState("approved");
	const [prefill] = useQueryState("user_code");

	if (approved) {
		return (
			<Shell>
				<div className="text-center" data-testid="claude-code-device-approved">
					<div className="bg-primary/10 mx-auto mb-4 flex size-14 items-center justify-center rounded-full">
						<CheckCircle2 className="text-primary size-7" />
					</div>
					<h1 className="text-xl font-semibold tracking-tight">{t("device.signedInTitle")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">{t("device.signedInBody")}</p>
				</div>
			</Shell>
		);
	}

	return <CodeEntry prefill={prefill ?? ""} />;
}

function CodeEntry({ prefill }: { prefill: string }) {
	const { t } = useTranslation("login");
	// Pre-filled from verification_uri_complete but never auto-submitted
	// (RFC 8628 §5.4): the user confirms the code matches their terminal.
	const [code, setCode] = useState(() => formatUserCode(prefill));
	const [error, setError] = useState<string | null>(null);
	const [submitting, setSubmitting] = useState(false);
	const complete = normalizeUserCode(code).length === USER_CODE_LENGTH;

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		if (!complete || submitting) return;
		setSubmitting(true);
		setError(null);
		try {
			const res = await fetch(getEndpointUrl("/gateway/claude-code/device/verify"), {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ user_code: code }),
			});
			const body = (await res.json().catch(() => null)) as { consent_url?: string; error?: { message?: string } } | null;
			if (!res.ok || !body?.consent_url) {
				setError(body?.error?.message || t("device.verifyFailed"));
				return;
			}
			// The consent URL is built by the server from the configured issuer, which
			// may be a different host than the one serving this page. Only refuse a
			// scheme that could execute script when assigned to location.href.
			const target = new URL(body.consent_url, window.location.origin);
			if (target.protocol !== "https:" && target.protocol !== "http:") {
				setError(t("device.invalidLink"));
				return;
			}
			window.location.href = target.toString();
		} catch {
			setError(t("device.unreachable"));
		} finally {
			setSubmitting(false);
		}
	};

	return (
		<Shell>
			<div className="mb-6 text-center">
				<div className="bg-primary/10 mx-auto mb-4 flex size-14 items-center justify-center rounded-full">
					<Terminal className="text-primary size-7" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{t("device.title")}</h1>
				<p className="text-muted-foreground mt-1.5 text-sm">{t("device.subtitle")}</p>
			</div>
			<form onSubmit={handleSubmit} className="space-y-3">
				<label htmlFor="claude-code-user-code" className="text-sm font-medium">
					{t("device.codeLabel")}
				</label>
				<Input
					id="claude-code-user-code"
					data-testid="claude-code-device-code-input"
					autoComplete="off"
					autoCapitalize="characters"
					spellCheck={false}
					placeholder="XXXX-XXXX" // i18n-ignore: the user-code format, not prose
					className="text-center font-mono text-lg tracking-widest"
					value={code}
					onChange={(e) => setCode(formatUserCode(e.target.value))}
					disabled={submitting}
					aria-invalid={error ? true : undefined}
					aria-describedby={error ? "claude-code-user-code-error" : undefined}
				/>
				{error && (
					<p id="claude-code-user-code-error" role="alert" className="text-destructive text-sm" data-testid="claude-code-device-error">
						{error}
					</p>
				)}
				<Button type="submit" className="w-full" disabled={!complete || submitting} data-testid="claude-code-device-continue-btn">
					{submitting ? (
						<>
							<Loader2 className="mr-2 size-4 animate-spin" />
							{t("device.checking")}
						</>
					) : (
						t("device.continue")
					)}
				</Button>
			</form>
		</Shell>
	);
}

function Shell({ children }: { children: React.ReactNode }) {
	return (
		<div className="mx-auto flex min-h-screen w-full items-center justify-center p-4 sm:p-6">
			<div className="bg-card w-full max-w-md rounded-sm border p-6 shadow-sm sm:p-8">{children}</div>
		</div>
	);
}