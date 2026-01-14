import Link from "next/link";

export default function NotFound() {
	return (
		<main className="h-base flex items-center justify-center p-6">
			<div className="mx-auto w-full max-w-md text-center">
				<p className="text-foreground text-7xl font-bold tracking-tight">404</p>
				<h1 className="text-foreground mt-4 text-2xl font-semibold">Page not found</h1>
				<p className="text-muted-foreground mt-2 text-sm">The page you are looking for doesn’t exist or has been moved</p>
				<div className="mt-6 flex items-center justify-center gap-3">
					<Link
						href="/workspace/logs"
						className="bg-primary text-primary-foreground focus-visible:ring-primary inline-flex items-center rounded-md px-4 py-2 text-sm font-medium shadow transition-opacity hover:opacity-90 focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none"
					>
						Go home
					</Link>
				</div>
			</div>
		</main>
	);
}
