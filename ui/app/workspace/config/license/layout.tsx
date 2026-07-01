import { createFileRoute } from "@tanstack/react-router";
import LicensePage from "./page";

export const Route = createFileRoute("/workspace/config/license")({
	component: LicensePage,
});