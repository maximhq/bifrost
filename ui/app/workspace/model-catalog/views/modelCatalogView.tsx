import { NoPermissionView } from "@/components/noPermissionView";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import AttributesTab from "./attributesTab";
import OverviewTab from "./overviewTab";

const MODEL_CATALOG_TABS = ["overview", "attributes"] as const;
type ModelCatalogTab = (typeof MODEL_CATALOG_TABS)[number];

export default function ModelCatalogView() {
	const hasAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);

	// Tab lives in the URL so a refresh (or a shared link) lands on the same tab.
	// Replace rather than push so tab clicks don't pile up in browser history.
	const [tab, setTab] = useQueryState(
		"tab",
		parseAsStringLiteral(MODEL_CATALOG_TABS).withDefault("overview").withOptions({ history: "replace" }),
	);

	if (!hasAccess) {
		return <NoPermissionView entity="model catalog" />;
	}

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] min-h-0 w-full max-w-7xl flex-col overflow-hidden p-4">
			<Tabs value={tab} onValueChange={(value) => setTab(value as ModelCatalogTab)} className="flex min-h-0 grow flex-col gap-4">
				<TabsList className="shrink-0">
					<TabsTrigger value="overview" data-testid="model-catalog-tab-overview">
						Overview
					</TabsTrigger>
					<TabsTrigger value="attributes" data-testid="model-catalog-tab-attributes">
						Models
					</TabsTrigger>
				</TabsList>
				<TabsContent value="overview" className="min-h-0 overflow-auto">
					<OverviewTab hasAccess={hasAccess} />
				</TabsContent>
				<TabsContent value="attributes" className="flex min-h-0 flex-col">
					<AttributesTab hasAccess={hasAccess} />
				</TabsContent>
			</Tabs>
		</div>
	);
}