import { createFileRoute } from "@tanstack/react-router";
import CachingPage from "./page";

export const Route = createFileRoute("/workspace/config/caching")({
	component: CachingPage,
});