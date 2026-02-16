import { notFound } from "next/navigation";
import { PprofClientLayout } from "./pprof-client-layout";

export default function PprofLayout({ children }: { children: React.ReactNode }) {
	if (process.env.NODE_ENV !== "development") {
		notFound();
	}

	return <PprofClientLayout>{children}</PprofClientLayout>;
}
