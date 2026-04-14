import { createFileRoute } from "@tanstack/react-router";
import ProxyPage from "./page";

export const Route = createFileRoute("/workspace/config/proxy")({
	component: ProxyPage,
});