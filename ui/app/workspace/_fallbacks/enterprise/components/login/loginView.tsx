"use client";
import FullPageLoader from "@/components/fullPageLoader";
import { useRouter } from "next/navigation";
import { useEffect } from "react";

export default function LoginView() {
	const router = useRouter();

	useEffect(() => {
		router.push("/");
	}, [router]);

	return <FullPageLoader />;
}
