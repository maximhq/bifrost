import { createFileRoute } from "@tanstack/react-router";
import GlobalLimitsPage from "./page";

export const Route = createFileRoute("/workspace/governance/global")({
  component: GlobalLimitsPage,
});
