import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { useBranding } from "@/lib/hooks/useBranding";
import { SETUP_TOKEN_HEADER } from "@/lib/store/apis/setupToken";
import { setupTokenFormSchema, SetupTokenFormSchema } from "@/lib/types/schemas";
import { getApiBaseUrl } from "@/lib/utils/port";
import { zodResolver } from "@hookform/resolvers/zod";
import { useNavigate } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { useTheme } from "next-themes";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { Trans, useTranslation } from "react-i18next";

// Where the operator lands after the token is accepted: the security page holds the
// existing admin-account form, which is the step that lifts the lock.
const SECURITY_SETTINGS_PATH = "/workspace/config/security";

const CONFIG_SNIPPET = `{
  "setup_token": "env.BIFROST_SETUP_TOKEN"
}`;

function SetupTokenInstructions() {
	const { t } = useTranslation("login");
	const code = <code className="text-foreground" />;
	return (
		<div className="text-muted-foreground space-y-3 text-sm" data-testid="setup-token-instructions">
			<p>{t("setup.instructionsIntro")}</p>
			<ul className="list-disc space-y-1 pl-5">
				<li>
					<Trans t={t} i18nKey="setup.instructionsEnv" components={{ code }} />
				</li>
				<li>
					<Trans t={t} i18nKey="setup.instructionsConfig" components={{ code }} />
				</li>
			</ul>
			<pre className="bg-muted text-foreground overflow-x-auto rounded-sm p-3 text-xs">{CONFIG_SNIPPET}</pre>
			<p>
				<Trans t={t} i18nKey="setup.instructionsHeader" values={{ header: SETUP_TOKEN_HEADER }} components={{ code }} />
			</p>
		</div>
	);
}

interface SetupTokenViewProps {
	setupTokenConfigured: boolean;
}

// SetupTokenView is the OSS setup-lock screen: dashboard auth is not active, so every /api
// call needs the operator's setup token. It verifies the token against a locked endpoint,
// stores it for this tab, and hands off to the security page to create the admin account.
export default function SetupTokenView({ setupTokenConfigured }: SetupTokenViewProps) {
	const { t } = useTranslation("login");
	const { resolvedTheme } = useTheme();
	const { logoSrc, logoAlt } = useBranding(resolvedTheme === "dark");
	const [helpOpen, setHelpOpen] = useState(false);
	const navigate = useNavigate();
	const form = useForm<SetupTokenFormSchema>({
		resolver: zodResolver(setupTokenFormSchema),
		defaultValues: { setup_token: "" },
	});

	const onSubmit = async ({ setup_token }: SetupTokenFormSchema) => {
		let res: Response;
		try {
			// Trade the token for an HttpOnly setup session cookie. The token is sent this
			// once and never kept by the dashboard.
			res = await fetch(`${getApiBaseUrl()}/session/setup`, {
				method: "POST",
				credentials: "include",
				headers: { [SETUP_TOKEN_HEADER]: setup_token },
			});
		} catch {
			form.setError("setup_token", { message: t("setup.unreachable") });
			return;
		}
		if (res.status === 401 || res.status === 403) {
			form.setError("setup_token", { message: t("setup.invalidToken") });
			return;
		}
		if (!res.ok) {
			form.setError("setup_token", { message: t("setup.checkFailed", { status: res.status }) });
			return;
		}
		form.reset();
		navigate({ to: SECURITY_SETTINGS_PATH });
	};

	return (
		<div className="flex min-h-screen items-center justify-center p-4">
			<div className="w-full max-w-md">
				<div className="border-border bg-card w-full space-y-6 rounded-sm border p-8" data-testid="setup-token-view">
					<div className="flex items-center justify-center">
						<img src={logoSrc} alt={logoAlt} width={160} height={26} className="max-h-[40px] w-auto max-w-[220px] object-contain" />
					</div>

					<div className="space-y-2 text-center">
						<h1 className="text-foreground text-lg font-semibold">{t("setup.title")}</h1>
						<p className="text-muted-foreground text-sm">{t("setup.description")}</p>
					</div>

					{setupTokenConfigured ? (
						<>
							<Form {...form}>
								<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-5">
									<FormField
										control={form.control}
										name="setup_token"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("setup.tokenLabel")}</FormLabel>
												<FormControl>
													<Input
														{...field}
														type="password"
														placeholder={t("setup.tokenPlaceholder")}
														autoComplete="off"
														className="text-sm"
														data-testid="setup-token-input"
													/>
												</FormControl>
												<FormMessage data-testid="setup-token-error" />
											</FormItem>
										)}
									/>
									<Button
										type="submit"
										className="h-9 w-full text-sm"
										isLoading={form.formState.isSubmitting}
										disabled={form.formState.isSubmitting}
										data-testid="setup-token-submit"
									>
										{t("setup.continue")}
									</Button>
								</form>
							</Form>
							<Collapsible open={helpOpen} onOpenChange={setHelpOpen}>
								<CollapsibleTrigger className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-sm transition-colors">
									{t("setup.whereToFind")}
									<ChevronDown className={`h-4 w-4 transition-transform ${helpOpen ? "rotate-180" : ""}`} />
								</CollapsibleTrigger>
								<CollapsibleContent className="pt-3">
									<SetupTokenInstructions />
								</CollapsibleContent>
							</Collapsible>
						</>
					) : (
						<div className="space-y-4">
							<div className="bg-destructive/10 text-destructive rounded-sm p-3 text-sm">{t("setup.notConfigured")}</div>
							<SetupTokenInstructions />
						</div>
					)}
				</div>
			</div>
		</div>
	);
}