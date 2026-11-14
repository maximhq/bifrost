import { useTranslation } from "react-i18next";
import { FormLabel } from "@/components/ui/form";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info } from "lucide-react";

/** The label side of the Code Mode Server row: name, docs link and where the setting applies. */
export function CodeModeServerLabel({ htmlFor }: { htmlFor?: string }) {
	const { t } = useTranslation("mcp");
	return (
		<div className="space-y-1">
			<div className="flex items-center gap-2">
				{/* An explicit undefined would override FormLabel's generated htmlFor. */}
				<FormLabel {...(htmlFor ? { htmlFor } : {})}>{t("registry.form.codeModeServer")}</FormLabel>
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<a
								href="https://docs.getbifrost.ai/mcp/code-mode"
								target="_blank"
								rel="noopener noreferrer"
								data-testid="code-mode-link-help"
								className="text-muted-foreground hover:text-foreground focus-visible:ring-ring rounded focus-visible:ring-2 focus-visible:outline-none"
								aria-label={t("registry.form.learnCodeModeAria")}
							>
								<Info className="h-4 w-4 cursor-help" />
							</a>
						</TooltipTrigger>
						<TooltipContent>
							<p>{t("registry.form.learnCodeMode")}</p>
						</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			</div>
			<p className="text-muted-foreground text-xs">{t("registry.form.codeModeScope")}</p>
		</div>
	);
}

/** The label side of the slug code mode row, naming the server's endpoint once it has a slug. */
export function ServerTypeOnSlugLabel({ slug }: { slug?: string }) {
	const { t } = useTranslation("mcp");
	const { t: tCommon } = useTranslation("common");
	const label = slug ? t("registry.form.serverTypeOnSlug", { slug }) : t("registry.form.serverTypeOnSlugOwnEndpoint");
	return (
		<div className="flex items-center gap-2">
			<FormLabel>{label}</FormLabel>
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							type="button"
							aria-label={tCommon("aboutField", { field: label })}
							className="inline-flex"
							data-testid="mcp-client-server-type-on-slug-help"
						>
							<Info className="text-muted-foreground h-4 w-4 cursor-help" />
						</button>
					</TooltipTrigger>
					<TooltipContent className="max-w-xs">
						<p>{t("registry.form.serverTypeOnSlugTooltip")}</p>
					</TooltipContent>
				</Tooltip>
			</TooltipProvider>
		</div>
	);
}