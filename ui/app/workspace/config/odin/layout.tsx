import { createFileRoute } from "@tanstack/react-router";
import OdinPage from "./page";

export const Route = createFileRoute("/workspace/config/odin")({
	component: OdinPage,
});