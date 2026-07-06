import { createFileRoute } from "@tanstack/react-router";
import ConnectorsPage from "./page";

export const Route = createFileRoute("/workspace/logs/connectors")({
	component: ConnectorsPage,
});