import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useFeatureFlag } from "@/hooks/useFeatureFlag";
import { FEATURE_FLAGS } from "@/lib/constants/featureFlags";
import { useListFeatureFlagsQuery } from "@/lib/store/apis/featureFlagsApi";
import { Link } from "@tanstack/react-router";
import { ArrowRight, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import WarpView from "../views/warpView";

export default function WarpPage() {
	const { isLoading } = useListFeatureFlagsQuery();
	const isWarpEnabled = useFeatureFlag(FEATURE_FLAGS.warp);

	// The sidebar hides this page while the flag is off, but the URL still
	// resolves. Every /api/warp route answers 404 in that state, so rendering the
	// settings form would only show a page of load errors.
	if (isLoading) return <FullPageLoader />;

	return <div className="no-padding-parent mx-auto flex w-full max-w-7xl p-4">{isWarpEnabled ? <WarpView /> : <WarpDisabled />}</div>;
}

function WarpDisabled() {
	const { t } = useTranslation("config");
	return (
		<div className="flex w-full flex-col gap-4">
			<PageTitle title="Warp" />
			<Alert variant="warning" data-testid="warp-feature-flag-disabled">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription className="gap-2">
					<span>{t("warp.featureFlagOff")}</span>
					<Button asChild variant="outline" size="sm" data-testid="warp-feature-flags-link">
						<Link to="/workspace/config/feature-flags">
							{t("warp.openFeatureFlags")}
							<ArrowRight className="size-3.5" />
						</Link>
					</Button>
				</AlertDescription>
			</Alert>
		</div>
	);
}