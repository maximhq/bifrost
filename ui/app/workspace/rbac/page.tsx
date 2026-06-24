import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

export default function RBACRedirectPage() {
	const navigate = useNavigate();
	useEffect(() => {
		navigate({ to: "/workspace/governance/rbac", replace: true });
	}, [navigate]);
	return null;
}