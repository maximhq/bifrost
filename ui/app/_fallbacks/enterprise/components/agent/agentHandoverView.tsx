import { CheckCircle2, CircleAlert } from "lucide-react";

export default function AgentHandoverView() {
	const status = new URLSearchParams(window.location.search).get("status");
	const isComplete = !status || status === "complete";
	const Icon = isComplete ? CheckCircle2 : CircleAlert;

	return (
		<main className="bg-background text-foreground flex min-h-screen items-center justify-center p-6">
			<section className="bg-card w-full max-w-xl rounded-sm border p-8 text-center shadow-sm">
				<div className="bg-primary/10 mx-auto mb-5 flex size-12 items-center justify-center rounded-full">
					<Icon className="text-primary size-6" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{isComplete ? "Bifrost Agent sign-in complete" : "Bifrost Agent sign-in"}</h1>
				<p className="text-muted-foreground mt-2 text-sm">
					{isComplete ? "You can close this window and return to the Bifrost Agent." : `Sign-in status: ${status}`}
				</p>
			</section>
		</main>
	);
}