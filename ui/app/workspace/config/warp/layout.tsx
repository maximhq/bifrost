import { createFileRoute } from "@tanstack/react-router";
import WarpPage from "./page";

export const Route = createFileRoute("/workspace/config/warp")({
	component: WarpPage,
});