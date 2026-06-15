import { createFileRoute } from "@tanstack/react-router";
import APIKeysPage from "./page";

export const Route = createFileRoute("/workspace/config/api-keys")({
	component: APIKeysPage,
});