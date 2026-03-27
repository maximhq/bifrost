"use client";

import { redirect, useSearchParams } from "next/navigation";

export default function PromptsPage() {
	const searchParams = useSearchParams();
	const queryString = searchParams.toString();
	redirect(`/workspace/prompt-repo${queryString ? `?${queryString}` : ""}`);
}
