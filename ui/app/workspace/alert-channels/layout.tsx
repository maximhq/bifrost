import { createFileRoute } from "@tanstack/react-router";
import AlertChannelsPage from "./page";

export const Route = createFileRoute("/workspace/alert-channels")({
	component: AlertChannelsPage,
});