import { createFileRoute } from "@tanstack/react-router";
import SecurityPage from "./page";

export const Route = createFileRoute("/workspace/config/security")({
	component: SecurityPage,
});