import { createFileRoute } from "@tanstack/react-router";
import OAuthGrantsPage from "./page";

export const Route = createFileRoute("/workspace/oauth-grants")({
	component: OAuthGrantsPage,
});
