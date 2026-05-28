import { NoPermissionView } from "@/components/noPermissionView";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import AttributesTab from "./attributesTab";
import OverviewTab from "./overviewTab";

export default function ModelCatalogView() {
	const hasAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);

	if (!hasAccess) {
		return <NoPermissionView entity="model catalog" />;
	}

	return (
		<div className="mx-auto w-full max-w-7xl">
			<Tabs defaultValue="overview" className="space-y-4">
				<TabsList>
					<TabsTrigger value="overview" data-testid="model-catalog-tab-overview">
						Overview
					</TabsTrigger>
					<TabsTrigger value="attributes" data-testid="model-catalog-tab-attributes">
						Models
					</TabsTrigger>
				</TabsList>
				<TabsContent value="overview" className="mt-6">
					<OverviewTab hasAccess={hasAccess} />
				</TabsContent>
				<TabsContent value="attributes" className="mt-6">
					<AttributesTab hasAccess={hasAccess} />
				</TabsContent>
			</Tabs>
		</div>
	);
}
