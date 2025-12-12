"use client";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ArrowUpRight } from "lucide-react";

interface Props {
	className?: string;
	icon: React.ReactNode;
	title: string;
	description: string;
	readmeLink: string;
	align?: "middle" | "top";
}

export default function ContactUsView({ icon, title, description, className, readmeLink, align = "middle" }: Props) {
	return (
		<div className={cn("flex flex-col items-center gap-4 text-center", align === "middle" ? "justify-center" : "justify-start", className)}>
			<div className="text-muted-foreground">{icon}</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{title}</h1>
				<div className="text-muted-foreground mt-2 max-w-[600px] text-sm font-normal">{description}</div>
				<div className="mx-auto flex flex-row items-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about this feature (opens in new tab)"
						className="mx-auto mt-6"
						onClick={() => {
							window.open(`${readmeLink}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						className="mx-auto mt-6"
						aria-label="Book a demo (opens Calendly in new tab)"
						onClick={() => {
							window.open("https://calendly.com/maximai/bifrost-demo?utm_source=bfd_ent", "_blank", "noopener,noreferrer");
						}}
					>
						Book a demo
					</Button>
				</div>
			</div>
		</div>
	);
}
