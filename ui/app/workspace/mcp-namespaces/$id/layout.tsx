import { createFileRoute } from "@tanstack/react-router";
import NamespaceDetailPage from "./page";

function RouteComponent() {
	const { id } = Route.useParams();
	return <NamespaceDetailPage id={Number(id)} />;
}

export const Route = createFileRoute("/workspace/mcp-namespaces/$id")({
	component: RouteComponent,
});
