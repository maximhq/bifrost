import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

export default function VirtualKeysRedirectPage() {
	const navigate = useNavigate();
	useEffect(() => {
		navigate({ to: "/workspace/governance/virtual-keys", replace: true });
	}, [navigate]);
	return null;
}