"use client";

import React from "react";
import { cn } from "@/lib/utils";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";

export interface TimeValue {
	hour: number;
	minute: number;
}

interface TimePickerProps {
	value?: TimeValue;
	onChange?: (value: TimeValue) => void;
	className?: string;
	"aria-label"?: string;
}

export const TimePicker = React.forwardRef<HTMLDivElement, TimePickerProps>((props, forwardedRef) => {
	const { value, onChange, className } = props;

	// Convert 24-hour to 12-hour format
	const hour24 = value?.hour ?? 0;
	const hour12 = hour24 === 0 ? 12 : hour24 > 12 ? hour24 - 12 : hour24;
	const period = hour24 >= 12 ? "PM" : "AM";
	const minute = value?.minute ?? 0;

	const handleHourChange = (newHourStr: string) => {
		if (!onChange) return;
		const newHour = Number(newHourStr);
		// Convert 12-hour back to 24-hour
		let hour24 = newHour;
		if (period === "PM" && newHour !== 12) {
			hour24 = newHour + 12;
		} else if (period === "AM" && newHour === 12) {
			hour24 = 0;
		}
		onChange({ hour: hour24, minute });
	};

	const handleMinuteChange = (newMinuteStr: string) => {
		if (!onChange) return;
		onChange({ hour: hour24, minute: Number(newMinuteStr) });
	};

	const handlePeriodChange = (newPeriod: "AM" | "PM") => {
		if (!onChange) return;
		let newHour24 = hour24;
		if (newPeriod === "PM" && hour24 < 12) {
			newHour24 = hour24 + 12;
		} else if (newPeriod === "AM" && hour24 >= 12) {
			newHour24 = hour24 - 12;
		}
		onChange({ hour: newHour24, minute });
	};

	return (
		<div
			ref={forwardedRef}
			className={cn(
				"inline-flex h-9 w-full items-center gap-1",
				className,
			)}
		>
			<Select value={hour12.toString()} onValueChange={handleHourChange}>
				<SelectTrigger size="sm" className="w-[70px] h-9">
					<SelectValue placeholder="HH" />
				</SelectTrigger>
				<SelectContent>
					{Array.from({ length: 12 }, (_, i) => i + 1).map((h) => (
						<SelectItem key={h} value={h.toString()}>
							{h.toString().padStart(2, "0")}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<span className="text-muted-foreground">:</span>
			<Select value={minute.toString()} onValueChange={handleMinuteChange}>
				<SelectTrigger size="sm" className="w-[70px] h-9">
					<SelectValue placeholder="MM" />
				</SelectTrigger>
				<SelectContent>
					{Array.from({ length: 60 }, (_, i) => i).map((m) => (
						<SelectItem key={m} value={m.toString()}>
							{m.toString().padStart(2, "0")}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Select value={period} onValueChange={(v) => handlePeriodChange(v as "AM" | "PM")}>
				<SelectTrigger size="sm" className="w-[70px] h-9">
					<SelectValue placeholder="AM" />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="AM">AM</SelectItem>
					<SelectItem value="PM">PM</SelectItem>
				</SelectContent>
			</Select>
		</div>
	);
});

TimePicker.displayName = "TimePicker";
