import { createFileRoute } from "@tanstack/react-router";
import PiiRedactorRulesPage from "./page";

export const Route = createFileRoute("/workspace/pii-redactor/rules")({
	component: PiiRedactorRulesPage,
});