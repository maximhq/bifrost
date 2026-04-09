import { createFileRoute } from '@tanstack/react-router';
import PromptsPage from "./page";

export const Route = createFileRoute('/workspace/prompt-repo')({
  component: PromptsPage,
})
