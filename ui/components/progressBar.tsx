import { useRouter } from "@tanstack/react-router";
import { BProgress } from "@bprogress/core";
import { useEffect } from "react";

/**
 * App-wide top progress bar driven by TanStack Router navigation events.
 * Replaces @bprogress/next/app, which only worked with the Next.js router.
 *
 * Subscribes to the router's pending state via subscribe() and toggles the
 * @bprogress/core bar accordingly.
 */
const AppProgressProvider = ({ children }: { children: React.ReactNode }) => {
	const router = useRouter();

	useEffect(() => {
		BProgress.configure({ showSpinner: false, minimum: 0.1 });

		const unsubBefore = router.subscribe("onBeforeLoad", () => {
			BProgress.start();
		});
		const unsubLoad = router.subscribe("onLoad", () => {
			BProgress.done();
		});

		return () => {
			unsubBefore();
			unsubLoad();
		};
	}, [router]);

	return <>{children}</>;
};

export default AppProgressProvider;