import { createFileRoute } from "@tanstack/react-router";
import AlertHistoryPage from "./page";

export const Route = createFileRoute("/workspace/alerting/history")({
  component: AlertHistoryPage,
});
