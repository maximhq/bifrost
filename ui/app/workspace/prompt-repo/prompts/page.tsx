import { useLocation, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

export default function PromptsPage() {
	const navigate = useNavigate();
	const { searchStr } = useLocation();

	useEffect(() => {
		navigate({ to: `/workspace/prompt-repo${searchStr}`, replace: true });
	}, [navigate, searchStr]);

	return null;
}
