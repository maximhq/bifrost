import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";

// RoutersNotice tells operators that Routers succeed this page. Nothing moves
// on its own: this page keeps routing as it does, and Routers start empty. The
// link to Routers shows only to those who can open that page.
export function RoutersNotice() {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.routersNotice" });
	const canViewRouters = useRbac(RbacResource.Routers, RbacOperation.View);
	return (
		<Alert variant="info" className="mb-4" data-testid="complexity-router-routers-notice">
			<Sparkles className="h-4 w-4" />
			<AlertTitle>{t("title")}</AlertTitle>
			<AlertDescription className="flex flex-wrap items-center justify-between gap-3">
				<span>{t("body")}</span>
				{canViewRouters && (
					<Button asChild variant="outline" size="sm" data-testid="complexity-router-routers-notice-link">
						<Link to="/workspace/model-routers">
							{t("action")}
							<ArrowRight className="size-3.5" />
						</Link>
					</Button>
				)}
			</AlertDescription>
		</Alert>
	);
}