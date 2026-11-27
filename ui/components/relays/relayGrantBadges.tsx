import { Badge } from "@/components/ui/badge";
import { useGetRelayQuery } from "@/lib/store/apis/relaysApi";
import type { RelayGrant } from "@/lib/types/relays";
import { describeRelayRoute, isAllRoutes } from "@/lib/utils/relayGrants";
import { useTranslation } from "react-i18next";

/** Read-only relay grants: one badge per relay, naming its routes or "All routes". */
export function RelayGrantBadges({ grants, testIdPrefix }: { grants: RelayGrant[]; testIdPrefix: string }) {
	return (
		<div className="flex flex-wrap gap-1">
			{grants.map((grant) => (
				<RelayGrantBadge key={grant.relay_name} grant={grant} testId={`${testIdPrefix}-${grant.relay_name}`} />
			))}
		</div>
	);
}

/** One relay's badge; a route list is resolved to method and path through the relay itself. */
function RelayGrantBadge({ grant, testId }: { grant: RelayGrant; testId: string }) {
	const { t } = useTranslation("observability");
	const all = isAllRoutes(grant.routes);
	const { data: relay } = useGetRelayQuery(grant.relay_name, { skip: all });
	const routes = all
		? t("relayLogs.grants.allRoutes")
		: (grant.routes ?? [])
				.map((id) => {
					const route = relay?.routes?.find((r) => r.id === id);
					return route ? describeRelayRoute(route) : relay ? t("relayLogs.grants.deletedRoute") : id;
				})
				.join(", ");
	return (
		<Badge variant="secondary" className="max-w-full truncate text-xs" title={routes} data-testid={testId}>
			{grant.relay_name} · {routes}
		</Badge>
	);
}