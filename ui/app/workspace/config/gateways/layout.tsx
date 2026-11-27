import { createFileRoute } from "@tanstack/react-router";
import GatewaysPage from "./page";

export const Route = createFileRoute("/workspace/config/gateways")({
	component: GatewaysPage,
});