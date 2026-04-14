import { createFileRoute } from "@tanstack/react-router";
import PiiRedactorProvidersPage from "./page";

export const Route = createFileRoute("/workspace/pii-redactor/providers")({
	component: PiiRedactorProvidersPage,
});