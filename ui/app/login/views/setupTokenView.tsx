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

// Where the operator lands after the token is accepted: the security page holds the
// existing admin-account form, which is the step that lifts the lock.
const SECURITY_SETTINGS_PATH = "/workspace/config/security";

const CONFIG_SNIPPET = `{
  "setup_token": "env.BIFROST_SETUP_TOKEN"
}`;

function SetupTokenInstructions() {
	return (
		<div className="text-muted-foreground space-y-3 text-sm" data-testid="setup-token-instructions">
			<p>The setup token is a secret the operator sets on the server. Set it one of two ways, then restart Bifrost:</p>
			<ul className="list-disc space-y-1 pl-5">
				<li>
					Set the <code className="text-foreground">BIFROST_SETUP_TOKEN</code> environment variable.
				</li>
				<li>
					Add <code className="text-foreground">setup_token</code> at the top level of <code className="text-foreground">config.json</code>.
					It accepts a literal value, an <code className="text-foreground">env.VAR</code> reference, or a{" "}
					<code className="text-foreground">vault.path</code> reference:
				</li>
			</ul>
			<pre className="bg-muted text-foreground overflow-x-auto rounded-sm p-3 text-xs">{CONFIG_SNIPPET}</pre>
			<p>
				API clients send the same value in the <code className="text-foreground">{SETUP_TOKEN_HEADER}</code> header. The token stops working
				once dashboard authentication is enabled.
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
			form.setError("setup_token", { message: "Could not reach Bifrost. Check that the server is running." });
			return;
		}
		if (res.status === 401 || res.status === 403) {
			form.setError("setup_token", { message: "Invalid setup token" });
			return;
		}
		if (!res.ok) {
			form.setError("setup_token", { message: `Bifrost returned ${res.status} while checking the token` });
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
						<h1 className="text-foreground text-lg font-semibold">Finish setting up Bifrost</h1>
						<p className="text-muted-foreground text-sm">
							Dashboard authentication is not configured, so the API is locked. Enter the setup token to continue and create an admin
							account.
						</p>
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
												<FormLabel>Setup token</FormLabel>
												<FormControl>
													<Input
														{...field}
														type="password"
														placeholder="Enter the setup token"
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
										Continue
									</Button>
								</form>
							</Form>
							<Collapsible open={helpOpen} onOpenChange={setHelpOpen}>
								<CollapsibleTrigger className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-sm transition-colors">
									Where do I find the setup token?
									<ChevronDown className={`h-4 w-4 transition-transform ${helpOpen ? "rotate-180" : ""}`} />
								</CollapsibleTrigger>
								<CollapsibleContent className="pt-3">
									<SetupTokenInstructions />
								</CollapsibleContent>
							</Collapsible>
						</>
					) : (
						<div className="space-y-4">
							<div className="bg-destructive/10 text-destructive rounded-sm p-3 text-sm">
								No setup token is configured on this server, so setup cannot continue yet.
							</div>
							<SetupTokenInstructions />
						</div>
					)}
				</div>
			</div>
		</div>
	);
}