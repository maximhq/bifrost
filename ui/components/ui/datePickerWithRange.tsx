"use client";

import { cn } from "@/lib/utils";
import { format } from "date-fns";
import { Calendar as CalendarIcon } from "lucide-react";
import React, { useEffect, useMemo } from "react";
import { DateRange } from "react-day-picker";
import { Button } from "./button";
import { Calendar } from "./calendar";
import { Label } from "./label";
import { Popover, PopoverContent, PopoverTrigger } from "./popover";
import { TimePicker, TimeValue } from "./timePicker";

export type TimeRange = {
	from: TimeValue;
	to: TimeValue;
};

interface DatePickerWithRangeProps extends React.HTMLAttributes<HTMLDivElement> {
	buttonClassName?: string;
	triggerLabel?: string;
	onTrigger?: (
		e: React.MouseEvent<HTMLButtonElement>,
		range: {
			from: { date?: Date; time: TimeValue };
			to: { date: Date; time: TimeValue };
		},
	) => void;
}

interface DateTimePickerWithRangeProps extends DatePickerWithRangeProps {
	popupAlignment?: "start" | "end" | "center";
	onDateTimeUpdate?: (date: DateRange) => void;
	onPredefinedPeriodChange?: (period: string | undefined) => void;
	dateTime?: DateRange;
	preDefinedPeriods?: { label: string; value: string }[];
	predefinedPeriod?: string;
	disabledBefore?: Date;
	disabledAfter?: Date;
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	/** Optional data-testid for the trigger button (e.g. for E2E tests) */
	triggerTestId?: string;
}

export function DateTimePickerWithRange(props: DateTimePickerWithRangeProps) {
	const { className, buttonClassName, triggerLabel, onTrigger, dateTime } = props;
	const [date, setDate] = React.useState<DateRange | undefined>(dateTime);
	const [timeValue, setTimeValue] = React.useState<TimeRange>({
		from: dateTime?.from ? { hour: dateTime.from.getHours(), minute: dateTime.from.getMinutes() } : { hour: 0, minute: 0 },
		to: dateTime?.to ? { hour: dateTime.to.getHours(), minute: dateTime.to.getMinutes() } : { hour: 23, minute: 59 },
	});
	const [isOpen, setIsOpen] = React.useState<boolean>(false);
	const [predefinedPeriod, setPredefinedPeriod] = React.useState<string | undefined>(props.predefinedPeriod);
	const disabledDateRange = useMemo(() => {
		if (!props.disabledBefore && !props.disabledAfter) return undefined;
		let range: any = {};
		if (props.disabledBefore) range["before"] = props.disabledBefore;
		if (props.disabledAfter) range["after"] = props.disabledAfter;
		return range;
	}, [props.disabledBefore, props.disabledAfter]);

	const printTimeValue = (timeObj: TimeValue): string => {
		// Validate input
		if (!timeObj || timeObj.hour < 0 || timeObj.hour >= 24 || timeObj.minute < 0 || timeObj.minute >= 60) {
			return "";
		}

		let hour = ((timeObj.hour + 11) % 12) + 1; // Convert hour to 12-hour format
		let period = timeObj.hour >= 12 ? "PM" : "AM"; // Determine AM/PM
		let minute = timeObj.minute.toString().padStart(2, "0"); // Ensure the minute has two digits

		return `${hour}:${minute} ${period}`;
	};

	const getDateTime = (date: Date | undefined, time: TimeValue | undefined | null): Date | undefined => {
		if (!date || !time) return undefined;

		// Create a date object from the selected calendar date (which is at midnight local time)
		const localDate = new Date(date);

		// Manually set the time in the local timezone. This is more robust than using `new Date(y,m,d,h,m)`.
		localDate.setHours(time.hour, time.minute, 0, 0);

		// new Date(year, month, day, hour, minute) can be problematic.
		// A more robust way is to get the epoch time for the date at midnight,
		// then add the hours and minutes in milliseconds.
		const dateAtMidnight = new Date(date.getFullYear(), date.getMonth(), date.getDate());
		const epochTime = dateAtMidnight.getTime() + time.hour * 60 * 60 * 1000 + time.minute * 60 * 1000;

		// Create a new Date object from the calculated epoch time.
		return new Date(epochTime);
	};

	useEffect(() => {
		setDate(dateTime);
		setTimeValue({
			from: dateTime?.from ? { hour: dateTime.from.getHours(), minute: dateTime.from.getMinutes() } : { hour: 0, minute: 0 },
			to: dateTime?.to ? { hour: dateTime.to.getHours(), minute: dateTime.to.getMinutes() } : { hour: 23, minute: 59 },
		});
	}, [dateTime]);

	useEffect(() => {
		setPredefinedPeriod(props.predefinedPeriod);
	}, [props.predefinedPeriod]);

	return (
		<div className={cn("grid gap-2", className)}>
			<Popover
				open={props.open !== undefined ? props.open : isOpen}
				onOpenChange={(open) => {
					setIsOpen(open);
					props.onOpenChange && props.onOpenChange(open);
				}}
			>
				<PopoverTrigger asChild>
					<Button
						id="date"
						variant="outline"
						data-testid={props.triggerTestId}
						className={cn(
							!predefinedPeriod && "w-[360px]",
							predefinedPeriod && "w-[140px]",
							"justify-start text-left font-normal",
							!date && "text-content-disabled",
							buttonClassName,
							isOpen && "border-black",
						)}
					>
						<CalendarIcon className="h-4 w-4" strokeWidth={1.5} />
						{predefinedPeriod ? (
							<span>{props.preDefinedPeriods?.find((p) => p.value === predefinedPeriod)?.label}</span>
						) : (
							<>
								{dateTime?.from ? (
									dateTime.to ? (
										<>
											{format(dateTime.from, "LLL dd, y")} {printTimeValue(timeValue?.from)} - {format(dateTime.to, "LLL dd, y")}{" "}
											{printTimeValue(timeValue?.to)}
										</>
									) : (
										format(dateTime.from, "LLL dd, y")
									)
								) : (
									<span>Pick a date</span>
								)}
							</>
						)}
					</Button>
				</PopoverTrigger>
				<PopoverContent className="w-auto p-0" align={props.popupAlignment ? props.popupAlignment : "start"}>
					<div className="flex flex-row gap-2">
						<div>
							<Calendar
								autoFocus
								mode="range"
								disabled={disabledDateRange}
								defaultMonth={date?.from}
								selected={date}
								onSelect={(range) => {
									if (!range) return;
									if (!range.to) {
										// here user has selected single date
										range.to = range.from;
									}
									setDate(range);
									setPredefinedPeriod(undefined);
									// Checking if range is different than props.dateTime
									if (
										range.from?.toISOString() !== props.dateTime?.from?.toISOString() ||
										range.to?.toISOString() !== props.dateTime?.to?.toISOString()
									) {
										props.onPredefinedPeriodChange && props.onPredefinedPeriodChange(undefined);
										// Checking if range is valid
										props.onDateTimeUpdate &&
											props.onDateTimeUpdate({
												from: getDateTime(range.from, timeValue?.from),
												to: getDateTime(range.to, timeValue?.to),
											});
									}
								}}
								numberOfMonths={2}
							/>
							<div className="-mt-1 flex flex-row items-center px-2 pb-1">
								<div className="m-1 flex flex-1 flex-col gap-1">
									<Label className="ml-0.5">From Time</Label>
									<TimePicker
										aria-label="From Time"
										className=""
										value={timeValue?.from}
										onChange={(v) => {
											if (!date || !date.from) return;
											if (v) setTimeValue({ from: v, to: timeValue.to });
											const from = new Date(date.from);
											if (v) from.setHours(v.hour, v.minute);
											setDate({ from: from, to: date.to });
											if (from.toISOString() !== props.dateTime?.from?.toISOString()) {
												// Checking if range is valid
												props.onDateTimeUpdate &&
													props.onDateTimeUpdate({
														from: getDateTime(from, v),
														to: getDateTime(date.to, timeValue?.to),
													});
											}
										}}
									/>
								</div>
								<div className="m-1 flex flex-1 flex-col gap-1">
									<Label className="ml-0.5">To Time</Label>
									<TimePicker
										aria-label="To Time"
										className=""
										value={timeValue?.to}
										onChange={(v) => {
											if (!date || !date.to) return;
											if (v) setTimeValue({ ...timeValue, to: v });
											const to = new Date(date.to);
											if (v) to.setHours(v.hour, v.minute);
											setDate({ from: date.from, to: to });
											if (to.toISOString() !== props.dateTime?.to?.toISOString()) {
												props.onDateTimeUpdate &&
													props.onDateTimeUpdate({
														from: getDateTime(date.from, timeValue?.from),
														to: getDateTime(to, v),
													});
											}
										}}
									/>
								</div>
							</div>
						</div>
						{props.preDefinedPeriods && (
							<div className="flex w-[150px] flex-col gap-1 border-l py-2 pr-3 pl-2">
								{props.preDefinedPeriods.map((period) => (
									<Button
										className={cn("w-full text-start text-sm", predefinedPeriod === period.value && "bg-primary text-primary-foreground")}
										variant="ghost"
										key={period.value}
										onClick={(e) => {
											e.preventDefault();
											e.stopPropagation();
											setPredefinedPeriod(period.value);
											props.onPredefinedPeriodChange && props.onPredefinedPeriodChange(period.value);
										}}
									>
										{period.label}
									</Button>
								))}
							</div>
						)}
					</div>
					{triggerLabel && onTrigger && (
						<div className="mt-1 mb-2 flex w-full px-3">
							<Button
								className="ml-auto"
								onClick={(e) => {
									if (!date || !date.from || !date.to) return;
									onTrigger(e, {
										from: { date: date.from, time: timeValue.from },
										to: { date: date.to, time: timeValue.to },
									});
								}}
							>
								{triggerLabel}
							</Button>
						</div>
					)}
				</PopoverContent>
			</Popover>
		</div>
	);
}

interface DateTimePickerProps extends React.HTMLAttributes<HTMLDivElement> {
	buttonClassName?: string;
	triggerLabel?: string;
	onTrigger?: (e: React.MouseEvent<HTMLButtonElement>, dateTime: { date?: Date; time: TimeValue }) => void;
	popupAlignment?: "start" | "end" | "center";
	onDateTimeUpdate?: (dateTime: Date) => void;
	dateTime?: Date;
	disabledBefore?: Date;
	disabledAfter?: Date;
}

export function DateTimePicker(props: DateTimePickerProps) {
	const { className, buttonClassName, triggerLabel, onTrigger, dateTime } = props;

	const initialDate = dateTime ? new Date(dateTime) : new Date();
	const [date, setDate] = React.useState<Date | undefined>(initialDate);
	const [timeValue, setTimeValue] = React.useState<TimeValue>({ hour: initialDate.getHours(), minute: initialDate.getMinutes() });
	const [isOpen, setIsOpen] = React.useState<boolean>(false);

	const disabledDateRange = useMemo(() => {
		if (!props.disabledBefore && !props.disabledAfter) return undefined;
		let range: any = {};
		if (props.disabledBefore) range["before"] = props.disabledBefore;
		if (props.disabledAfter) range["after"] = props.disabledAfter;
		return range;
	}, [props.disabledBefore, props.disabledAfter]);

	const printTimeValue = (timeObj: TimeValue): string => {
		// Validate input
		if (!timeObj || timeObj.hour < 0 || timeObj.hour >= 24 || timeObj.minute < 0 || timeObj.minute >= 60) {
			return "";
		}

		let hour = ((timeObj.hour + 11) % 12) + 1; // Convert hour to 12-hour format
		let period = timeObj.hour >= 12 ? "PM" : "AM"; // Determine AM/PM
		let minute = timeObj.minute.toString().padStart(2, "0"); // Ensure the minute has two digits

		return `${hour}:${minute} ${period}`;
	};

	const getDateTime = (date: Date | undefined | null, time: TimeValue | undefined | null): Date | undefined => {
		if (!date) return undefined;
		const dateTime = new Date(date);
		if (time) dateTime.setHours(time.hour, time.minute);
		return dateTime;
	};

	useEffect(() => {
		if (dateTime) {
			const newDate = new Date(dateTime);
			setDate(newDate);
			setTimeValue({ hour: newDate.getHours(), minute: newDate.getMinutes() });
		}
	}, [dateTime]);

	return (
		<div className={cn("grid gap-2", className)}>
			<Popover
				modal={true}
				onOpenChange={(open) => {
					setIsOpen(open);
				}}
			>
				<PopoverTrigger asChild>
					<Button
						id="date"
						variant="default"
						className={cn(
							"w-max justify-start text-left font-normal",
							!date && "text-content-disabled",
							buttonClassName,
							isOpen && "border-black",
						)}
					>
						<CalendarIcon className="h-4 w-4" strokeWidth={1.5} />
						{date ? (
							<>
								{format(date, "LLL dd, y")} {printTimeValue(timeValue)}
							</>
						) : (
							<span>Pick a date and time</span>
						)}
					</Button>
				</PopoverTrigger>
				<PopoverContent className="w-auto p-0" align={props.popupAlignment ? props.popupAlignment : "start"}>
					<div className="p-2">
						<Calendar
							autoFocus
							mode="single"
							disabled={disabledDateRange}
							defaultMonth={date}
							selected={date}
							onSelect={(selectedDate) => {
								if (!selectedDate) return;
								setDate(selectedDate);

								const newDateTime = getDateTime(selectedDate, timeValue);

								if (newDateTime?.toISOString() !== props.dateTime?.toISOString()) {
									props.onDateTimeUpdate && newDateTime && props.onDateTimeUpdate(newDateTime);
								}
							}}
						/>
						<div className="mt-3 flex flex-col gap-1 px-2 pb-2">
							<Label className="ml-0.5">Time</Label>
							<TimePicker
								aria-label="Time"
								className=""
								value={timeValue}
								onChange={(v) => {
									if (v) setTimeValue(v);

									const newDateTime = getDateTime(date, v);

									if (newDateTime?.toISOString() !== props.dateTime?.toISOString()) {
										props.onDateTimeUpdate && newDateTime && props.onDateTimeUpdate(newDateTime);
									}
								}}
							/>
						</div>
					</div>
					{triggerLabel && onTrigger && (
						<div className="mt-1 mb-2 flex w-full px-3">
							<Button
								className="ml-auto"
								onClick={(e) =>
									onTrigger(e, {
										date: date,
										time: timeValue,
									})
								}
							>
								{triggerLabel}
							</Button>
						</div>
					)}
				</PopoverContent>
			</Popover>
		</div>
	);
}
