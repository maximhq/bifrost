import { createFileRoute } from "@tanstack/react-router";
import AlertRulesPage from "./page";

export const Route = createFileRoute("/workspace/alerting/rules")({
  component: AlertRulesPage,
});