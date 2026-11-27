import { createFileRoute } from "@tanstack/react-router";
import SkillsSettingsPage from "./page";

export const Route = createFileRoute("/workspace/config/skills")({
	component: SkillsSettingsPage,
});